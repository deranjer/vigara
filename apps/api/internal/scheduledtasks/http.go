package scheduledtasks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5"
)

type Authorizer interface {
	Authorize(http.ResponseWriter, *http.Request, auth.Permission) (auth.User, bool)
}

func (s *Service) Register(mux *http.ServeMux, accounts Authorizer, recorder *audit.Recorder) {
	respond := func(w http.ResponseWriter, code int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("GET /api/admin/scheduled-tasks", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := accounts.Authorize(w, r, auth.PermTasksManage); !ok {
			return
		}
		tasks, err := s.List(r.Context())
		if err != nil {
			http.Error(w, "could not load scheduled tasks", 500)
			return
		}
		respond(w, 200, tasks)
	})
	mux.HandleFunc("POST /api/admin/scheduled-tasks/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		user, ok := accounts.Authorize(w, r, auth.PermTasksManage)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if _, ok := s.definitions[id]; !ok {
			http.NotFound(w, r)
			return
		}
		if recorder != nil {
			if err := recorder.Record(r.Context(), audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "scheduled_task.run", TargetKind: "scheduled_task", TargetID: id, Outcome: audit.OutcomeSuccess}); err != nil {
				http.Error(w, "could not audit task request", 500)
				return
			}
		}
		if err := s.RunNow(id); err != nil {
			code := 400
			if errors.Is(err, ErrRunning) {
				code = 409
			}
			http.Error(w, err.Error(), code)
			return
		}
		respond(w, 202, map[string]string{"status": "running"})
	})
	mux.HandleFunc("PATCH /api/admin/scheduled-tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		user, ok := accounts.Authorize(w, r, auth.PermTasksManage)
		if !ok {
			return
		}
		var input struct {
			Enabled  *bool           `json:"enabled"`
			Settings json.RawMessage `json:"settings"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Enabled == nil {
			http.Error(w, "invalid task settings", 400)
			return
		}
		id := r.PathValue("id")
		if err := s.Update(r.Context(), id, *input.Enabled, input.Settings); err != nil {
			code, message := 500, "could not save scheduled task settings"
			if errors.Is(err, pgx.ErrNoRows) {
				code, message = 404, "task not found"
			} else if errors.Is(err, ErrInvalidSettings) {
				code, message = 400, err.Error()
			}
			http.Error(w, message, code)
			return
		}
		if recorder != nil {
			_ = recorder.Record(r.Context(), audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "scheduled_task.configure", TargetKind: "scheduled_task", TargetID: id, Outcome: audit.OutcomeSuccess})
		}
		respond(w, 200, map[string]bool{"saved": true})
	})
}
