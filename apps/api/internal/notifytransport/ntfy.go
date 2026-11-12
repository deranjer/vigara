package notifytransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	NtfyURLVar     = "VIGIRA_NTFY_URL"
	NtfyTopicVar   = "VIGIRA_NTFY_TOPIC"
	NtfyTokenVar   = "VIGIRA_NTFY_TOKEN"
	NtfyTargetID   = "ntfy"
	maxNtfyMessage = 4096
)

var ntfyTopicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type ntfyAction struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
	Clear  bool   `json:"clear,omitempty"`
}
type ntfyPayload struct {
	Topic    string       `json:"topic"`
	Title    string       `json:"title"`
	Message  string       `json:"message"`
	Priority int          `json:"priority"`
	Tags     []string     `json:"tags,omitempty"`
	Click    string       `json:"click,omitempty"`
	Actions  []ntfyAction `json:"actions,omitempty"`
}

func newNtfyTarget(baseURL, topic, token string, client *http.Client) (Target, error) {
	endpoint, host, err := ntfyEndpoint(baseURL)
	if err != nil {
		return Target{}, errors.New("VIGIRA_NTFY_URL must be an HTTP or HTTPS server URL without credentials, query, or fragment")
	}
	if !ntfyTopicPattern.MatchString(topic) {
		return Target{}, errors.New("VIGIRA_NTFY_TOPIC must be 1-64 letters, digits, '-' or '_'")
	}
	if token != "" && !validGotifyToken(token) {
		return Target{}, errors.New("VIGIRA_NTFY_TOKEN must be a printable access token")
	}
	if client == nil {
		client = newHTTPClient()
	}
	return Target{
		ID:      NtfyTargetID,
		Kind:    "ntfy",
		Display: host + " · topic " + topic,
		send: func(ctx context.Context, message Message) error {
			request, err := newJSONRequest(ctx, endpoint, renderNtfy(topic, message))
			if err != nil {
				return &DeliveryError{target: NtfyTargetID, reason: "request_failed", temporary: true}
			}
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			return postJSON(client, request, NtfyTargetID, validNtfyAcknowledgement)
		},
	}, nil
}

// ntfyEndpoint returns the publish URL (the server root, which takes JSON with
// a topic field) and the host shown in settings.
func ntfyEndpoint(raw string) (string, string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", "", errors.New("invalid URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", "", errors.New("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", errors.New("invalid URL")
	}
	for _, part := range strings.Split(parsed.Path, "/") {
		if part == "." || part == ".." {
			return "", "", errors.New("invalid URL")
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	return parsed.String(), parsed.Scheme + "://" + parsed.Host, nil
}

// ntfy priorities: 5 urgent, 4 high, 3 default, 2 low.
func ntfyPriority(message Message) int {
	switch {
	case message.Transition == "test":
		return 3
	case message.Transition != "firing":
		return 2
	case message.Severity == "critical":
		return 5
	default:
		return 4
	}
}

func renderNtfy(topic string, message Message) ntfyPayload {
	rule := cleanField(message.RuleName, 120)
	summary := cleanField(message.Summary, 2400)
	device := cleanField(message.DeviceName, 120)
	title := rule
	tags := []string{"warning"}
	switch message.Transition {
	case "firing":
		if message.Severity == "critical" {
			tags = []string{"rotating_light"}
		}
	case "test":
		title, tags = "Vigara test notification", []string{"bell"}
	default:
		title, tags = rule+" cleared", []string{"white_check_mark"}
	}
	if title == "" {
		title = "Vigara alert"
	}
	body := summary
	if device != "" && device != "Device" && !strings.Contains(summary, device) {
		body = device + ": " + summary
	}
	payload := ntfyPayload{Topic: topic, Title: truncateUTF8(title, 200), Message: truncateUTF8(fallback(body), maxNtfyMessage), Priority: ntfyPriority(message), Tags: tags}
	if safeLink(message.OpenURL) {
		payload.Click = message.OpenURL
	}
	if message.Transition == "firing" {
		if safeLink(message.AcknowledgeURL) {
			payload.Actions = append(payload.Actions, ntfyAction{Action: "http", Label: "Acknowledge", URL: message.AcknowledgeURL, Method: "POST", Clear: true})
		}
		if safeLink(message.MuteURL) {
			payload.Actions = append(payload.Actions, ntfyAction{Action: "http", Label: "Mute 24 h", URL: message.MuteURL, Method: "POST", Clear: true})
		}
	}
	return payload
}

func safeLink(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

func validNtfyAcknowledgement(body []byte) bool {
	var response struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(body, &response) == nil && response.ID != ""
}
