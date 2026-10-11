package notifytransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

const maxGotifyTokenBytes = 4096

type gotifyPayload struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
}

func newGotifyTarget(baseURL, token string, client *http.Client) (Target, error) {
	endpoint, err := gotifyEndpoint(baseURL)
	if err != nil {
		return Target{}, errors.New("VIGIRA_GOTIFY_URL must be an HTTP or HTTPS base URL without credentials, query, or fragment")
	}
	if !validGotifyToken(token) {
		return Target{}, errors.New("VIGIRA_GOTIFY_TOKEN must be a non-empty printable application token")
	}
	if client == nil {
		client = newHTTPClient()
	}
	return Target{
		ID: GotifyTargetID,
		send: func(ctx context.Context, message Message) error {
			title, content := renderGotify(message)
			payload := gotifyPayload{Title: title, Message: content, Priority: 5}
			body, err := json.Marshal(payload)
			if err != nil || len(body) > maxGotifyPayloadBytes {
				return &DeliveryError{target: GotifyTargetID, reason: "payload_too_large", temporary: false}
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return &DeliveryError{target: GotifyTargetID, reason: "request_failed", temporary: true}
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Gotify-Key", token)
			return postJSON(client, request, GotifyTargetID, validGotifyAcknowledgement)
		},
	}, nil
}

func gotifyEndpoint(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", errors.New("invalid URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", errors.New("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("invalid URL")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("invalid URL")
	}
	for _, part := range strings.Split(parsed.Path, "/") {
		if part == "." || part == ".." {
			return "", errors.New("invalid URL")
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/message"
	return parsed.String(), nil
}

func validGotifyToken(token string) bool {
	if token == "" || len(token) > maxGotifyTokenBytes {
		return false
	}
	for _, r := range token {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func validGotifyAcknowledgement(body []byte) bool {
	var response struct {
		ID int64 `json:"id"`
	}
	return json.Unmarshal(body, &response) == nil && response.ID > 0
}
