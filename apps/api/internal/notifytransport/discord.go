package notifytransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type discordPayload struct {
	Content         string          `json:"content"`
	AllowedMentions discordMentions `json:"allowed_mentions"`
}

type discordMentions struct {
	Parse []string `json:"parse"`
}

func newDiscordTarget(rawURL string, client *http.Client) (Target, error) {
	endpoint, err := discordEndpoint(rawURL)
	if err != nil {
		return Target{}, errors.New("VIGIRA_DISCORD_WEBHOOK_URL must be an HTTPS discord.com webhook URL")
	}
	if client == nil {
		client = newHTTPClient()
	}
	return Target{
		ID: DiscordTargetID,
		send: func(ctx context.Context, message Message) error {
			payload := discordPayload{Content: renderDiscord(message), AllowedMentions: discordMentions{Parse: []string{}}}
			request, err := newJSONRequest(ctx, endpoint, payload)
			if err != nil {
				return &DeliveryError{target: DiscordTargetID, reason: "request_failed", temporary: true}
			}
			return postJSON(client, request, DiscordTargetID, validDiscordAcknowledgement)
		},
	}, nil
}

func discordEndpoint(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", errors.New("invalid URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "discord.com") {
		return "", errors.New("invalid URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "api" || parts[1] != "webhooks" || !allDigits(parts[2]) || parts[3] == "" || !validWebhookToken(parts[3]) {
		return "", errors.New("invalid URL")
	}
	parsed.Path = "/" + strings.Join(parts, "/")
	query := parsed.Query()
	query.Set("wait", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validWebhookToken(value string) bool {
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validDiscordAcknowledgement(body []byte) bool {
	var response struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(body, &response) == nil && allDigits(response.ID)
}
