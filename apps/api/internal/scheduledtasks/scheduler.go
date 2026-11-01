// Package scheduledtasks owns recurring server maintenance and persisted runs.
package scheduledtasks

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRunning = errors.New("task is already running")
var ErrUnavailable = errors.New("task is unavailable in this server configuration")
var ErrInvalidSettings = errors.New("invalid task settings")

type Definition struct {
	ID, Name, Description string
	Interval, Timeout     time.Duration
	Available             bool
	Defaults              json.RawMessage
	Validate              func(json.RawMessage) error
	Run                   func(context.Context, json.RawMessage) error
}

type Task struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Enabled         bool            `json:"enabled"`
	Available       bool            `json:"available"`
	RunOnStartup    bool            `json:"runOnStartup"`
	IntervalSeconds int             `json:"intervalSeconds"`
	Settings        json.RawMessage `json:"settings"`
	Status          string          `json:"status"`
	LastStartedAt   *time.Time      `json:"lastStartedAt"`
	LastFinishedAt  *time.Time      `json:"lastFinishedAt"`
	NextRunAt       *time.Time      `json:"nextRunAt"`
	LastError       string          `json:"lastError"`
}

type Service struct {
	mu          sync.Mutex
	stopping    bool
	wg          sync.WaitGroup
	pool        *pgxpool.Pool
	definitions map[string]Definition
	ctx         context.Context
	cancel      context.CancelFunc
	logger      *slog.Logger
}

func New(pool *pgxpool.Pool, logger *slog.Logger, definitions []Definition) *Service {
	s := &Service{pool: pool, logger: logger, definitions: map[string]Definition{}, ctx: context.Background()}
	for _, definition := range definitions {
		s.definitions[definition.ID] = definition
	}
	return s
}

// Start registers built-in jobs and makes every enabled available job due on
// every startup, regardless of its previously persisted next-run timestamp.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil || s.stopping {
		s.mu.Unlock()
		return errors.New("scheduled task service has already started or stopped")
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	ctx = s.ctx
	s.mu.Unlock()
	for _, d := range s.definitions {
		if len(d.Defaults) == 0 {
			d.Defaults = json.RawMessage(`{}`)
		}
		_, err := s.pool.Exec(ctx, `INSERT INTO scheduled_tasks(id,enabled,settings,interval_seconds)
            VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET interval_seconds=EXCLUDED.interval_seconds,next_run_at=now()`,
			d.ID, true, d.Defaults, int(d.Interval/time.Second))
		if err != nil {
			s.cancel()
			return err
		}
	}
	s.tick()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tick()
			}
		}
	}()
	return nil
}

func (s *Service) tick() {
	if s.ctx.Err() != nil {
		return
	}
	for id, d := range s.definitions {
		if !d.Available {
			continue
		}
		if err := s.start(id, false); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Error("scheduled task claim failed", "task", id, "error", err)
		}
	}
}

func (s *Service) RunNow(id string) error {
	if _, ok := s.definitions[id]; !ok {
		return pgx.ErrNoRows
	}
	err := s.start(id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunning
	}
	return err
}

func (s *Service) start(id string, manual bool) error {
	d, ok := s.definitions[id]
	if !ok {
		return pgx.ErrNoRows
	}
	if !d.Available {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return context.Canceled
	}
	var runID string
	var settings json.RawMessage
	claim, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	err := s.pool.QueryRow(claim, `UPDATE scheduled_tasks SET status='running',run_id=gen_random_uuid(),
		last_started_at=now(),last_finished_at=NULL,last_error='',next_run_at=now()+(interval_seconds*interval '1 second'),lease_until=now()+($3*interval '1 second')
        WHERE id=$1 AND (enabled OR $2) AND (next_run_at<=now() OR $2)
        AND (status<>'running' OR lease_until<now()) RETURNING run_id::text,settings`, id, manual, int(d.Timeout/time.Second)+60).Scan(&runID, &settings)
	cancel()
	if err != nil {
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		work, cancel := context.WithTimeout(s.ctx, d.Timeout)
		defer cancel()
		err := func() (err error) {
			defer func() {
				if recover() != nil {
					err = errors.New("task panicked; inspect server logs")
				}
			}()
			if d.Validate != nil {
				if err := d.Validate(settings); err != nil {
					return err
				}
			}
			return d.Run(work, settings)
		}()
		status, message := "succeeded", ""
		delay := d.Interval
		if err != nil {
			status, message = "failed", err.Error()
			if runes := []rune(message); len(runes) > 1000 {
				message = string(runes[:1000])
			}
			if delay > time.Minute {
				delay = time.Minute
			}
			s.logger.Error("scheduled task failed", "task", id, "error", err)
		}
		finish, cancelFinish := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelFinish()
		_, updateErr := s.pool.Exec(finish, `UPDATE scheduled_tasks SET status=$3,last_error=$4,last_finished_at=now(),
            next_run_at=GREATEST(now(),CASE WHEN $3='failed' THEN LEAST(next_run_at,now()+($5*interval '1 second')) ELSE next_run_at END),
            lease_until=NULL WHERE id=$1 AND run_id=$2::uuid`, id, runID, status, message, int(delay/time.Second))
		if updateErr != nil {
			s.logger.Error("could not record scheduled task result", "task", id, "error", updateErr)
		}
	}()
	return nil
}

func (s *Service) Close() {
	s.mu.Lock()
	s.stopping = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		s.logger.Error("scheduled tasks did not stop before shutdown deadline")
	}
}

func (s *Service) List(ctx context.Context) ([]Task, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,enabled,settings,interval_seconds,status,last_started_at,last_finished_at,next_run_at,last_error FROM scheduled_tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Task{}
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.Enabled, &task.Settings, &task.IntervalSeconds, &task.Status, &task.LastStartedAt, &task.LastFinishedAt, &task.NextRunAt, &task.LastError); err != nil {
			return nil, err
		}
		d, ok := s.definitions[task.ID]
		if !ok {
			continue
		}
		task.Name, task.Description, task.Available, task.RunOnStartup = d.Name, d.Description, d.Available, true
		if !task.Enabled || !task.Available {
			task.NextRunAt = nil
		}
		result = append(result, task)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, rows.Err()
}

func (s *Service) Update(ctx context.Context, id string, enabled bool, settings json.RawMessage) error {
	d, ok := s.definitions[id]
	if !ok {
		return pgx.ErrNoRows
	}
	if d.Validate != nil {
		if err := d.Validate(settings); err != nil {
			return errors.Join(ErrInvalidSettings, err)
		}
	} else {
		var values map[string]json.RawMessage
		if json.Unmarshal(settings, &values) != nil || values == nil || len(values) > 0 {
			return errors.Join(ErrInvalidSettings, errors.New("this task has no editable settings"))
		}
	}
	result, err := s.pool.Exec(ctx, `UPDATE scheduled_tasks SET enabled=$2,settings=$3,next_run_at=CASE WHEN $2 AND NOT enabled THEN now() ELSE next_run_at END WHERE id=$1`, id, enabled, settings)
	if err == nil && result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}
