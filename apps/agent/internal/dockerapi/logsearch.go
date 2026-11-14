package dockerapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

const maxLogScanBytes = 64 << 20

var errLogScanLimit = errors.New("log search scan limit reached")

// The HTTP API accepts Unix seconds with a fractional nanosecond component,
// unlike the CLI, which also accepts RFC3339 input. Keep cursor precision.
func dockerLogTimestamp(raw string) (string, error) {
	stamp, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", ErrBadRequest
	}
	return fmt.Sprintf("%d.%09d", stamp.Unix(), stamp.Nanosecond()), nil
}

type scanReader struct {
	r         io.Reader
	remaining int64
}

func (r *scanReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errLogScanLimit
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}

// SearchLogs filters on the host before bounding the returned matches. It reads
// retained history, not the last N noisy lines, and never invokes a shell.
func (c *Client) SearchLogs(ctx context.Context, p protocol.DockerLogSearchParams) (protocol.DockerLogSearchResult, error) {
	out := protocol.DockerLogSearchResult{Lines: []protocol.DockerLogLine{}}
	if checkRef(p.ID) != nil || len(p.Match) < 1 || len(p.Match) > 256 {
		return out, ErrBadRequest
	}
	for _, stamp := range []string{p.Since, p.Until} {
		if stamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				return out, ErrBadRequest
			}
		}
	}
	if p.Since != "" && p.Until != "" {
		s, _ := time.Parse(time.RFC3339Nano, p.Since)
		u, _ := time.Parse(time.RFC3339Nano, p.Until)
		if !s.Before(u) {
			return out, ErrBadRequest
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var meta struct{ Config struct{ Tty bool } }
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(p.ID)+"/json", nil, maxInspectBytes, &meta); err != nil {
		return out, err
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {"all"}}
	if p.Since != "" {
		stamp, _ := dockerLogTimestamp(p.Since)
		q.Set("since", stamp)
	}
	if p.Until != "" {
		stamp, _ := dockerLogTimestamp(p.Until)
		q.Set("until", stamp)
	}
	resp, err := c.open(ctx, "/containers/"+url.PathEscape(p.ID)+"/logs", q)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	reader := &scanReader{r: resp.Body, remaining: maxLogScanBytes}
	err = pumpLogs(reader, meta.Config.Tty, func(stream, text string, cut bool) error {
		stamp, message, hasStamp := strings.Cut(text, " ")
		if !hasStamp {
			return nil
		}
		t, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil
		}
		if out.NextSince == "" {
			out.NextSince = stamp
		} else {
			previous, _ := time.Parse(time.RFC3339Nano, out.NextSince)
			if t.After(previous) {
				out.NextSince = stamp
			}
		}
		if !strings.Contains(message, p.Match) {
			return nil
		}
		// Even worst-case JSON escaping of 200 lines must fit one channel frame.
		if len(message) > 512 {
			message = trimUTF8(message, 512)
			cut = true
		}
		line := protocol.DockerLogLine{S: stream, T: stamp, M: message, X: cut}
		if len(out.Lines) == 200 {
			copy(out.Lines, out.Lines[1:])
			out.Lines = out.Lines[:199]
			out.MoreMatches = true
		}
		out.Lines = append(out.Lines, line)
		return nil
	})
	out.ScannedBytes = maxLogScanBytes - reader.remaining
	if errors.Is(err, errLogScanLimit) || ctx.Err() != nil {
		out.Truncated = true
		return out, nil
	}
	if err != nil {
		return out, err
	}
	return out, nil
}
