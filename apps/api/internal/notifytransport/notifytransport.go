// Package notifytransport provides bounded HTTP transports for external
// notification destinations. Delivery policy and retries belong to callers.
package notifytransport

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	GotifyURLVar          = "VIGIRA_GOTIFY_URL"
	GotifyTokenVar        = "VIGIRA_GOTIFY_TOKEN"
	DiscordWebhookURLVar  = "VIGIRA_DISCORD_WEBHOOK_URL"
	GotifyTargetID        = "gotify"
	DiscordTargetID       = "discord"
	httpTimeout           = 10 * time.Second
	maxResponseBody       = 16 << 10
	maxResponseDrain      = 16 << 10
	maxGotifyPayloadBytes = 12 << 10
	maxRetryAfter         = 24 * time.Hour
)

// Message is the bounded, human-readable alert data delivered to a target.
// It deliberately contains no raw event payload.
type Message struct {
	EventID    string
	Transition string
	DeviceName string
	RuleName   string
	Summary    string
	OccurredAt time.Time
}

// Target is an enabled destination. ID is stable and safe to expose in
// operational status; Send never includes credentials or response bodies in
// its errors.
type Target struct {
	ID   string
	send func(context.Context, Message) error
}

// Send delivers one notification. Retry decisions are left to the durable
// worker so each destination has one consistent retry policy.
func (t Target) Send(ctx context.Context, message Message) error {
	if t.send == nil {
		return &DeliveryError{target: t.ID, reason: "unavailable", temporary: false}
	}
	return t.send(ctx, message)
}

// DeliveryError reports only a destination and stable error category. It never
// retains the original HTTP, URL, or body error because those may contain
// credentials.
type DeliveryError struct {
	target     string
	reason     string
	temporary  bool
	retryAfter time.Duration
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return "notification delivery failed"
	}
	return "notification delivery failed (" + safeTargetName(e.target) + ": " + e.reason + ")"
}

// Temporary indicates whether the caller may retry delivery.
func (e *DeliveryError) Temporary() bool { return e != nil && e.temporary }

// ErrorCode returns the stable category suitable for worker status storage.
func (e *DeliveryError) ErrorCode() string {
	if e == nil {
		return "notification_failed"
	}
	return e.reason
}

// RetryAfter returns a bounded server-requested delay, or zero when none was
// provided.
func (e *DeliveryError) RetryAfter() time.Duration {
	if e == nil {
		return 0
	}
	return e.retryAfter
}

func safeTargetName(value string) string {
	switch value {
	case GotifyTargetID:
		return GotifyTargetID
	case DiscordTargetID:
		return DiscordTargetID
	default:
		return "notification"
	}
}

// FromEnv creates the configured adapters. A destination is disabled when all
// of its settings are absent; partial settings fail startup rather than silently
// dropping notifications.
func FromEnv() ([]Target, error) {
	return fromEnv(os.Getenv)
}

func fromEnv(getenv func(string) string) ([]Target, error) {
	gotifyURL := strings.TrimSpace(getenv(GotifyURLVar))
	gotifyToken := getenv(GotifyTokenVar)
	discordURL := strings.TrimSpace(getenv(DiscordWebhookURLVar))

	targets := make([]Target, 0, 2)
	if gotifyURL == "" && gotifyToken == "" {
		// Disabled.
	} else {
		if gotifyURL == "" || gotifyToken == "" {
			return nil, errors.New("Gotify requires both VIGIRA_GOTIFY_URL and VIGIRA_GOTIFY_TOKEN")
		}
		target, err := newGotifyTarget(gotifyURL, gotifyToken, newHTTPClient())
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}

	if discordURL != "" {
		target, err := newDiscordTarget(discordURL, newHTTPClient())
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
