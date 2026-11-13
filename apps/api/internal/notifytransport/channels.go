package notifytransport

import (
	"errors"
	"strings"
)

// Channel kinds that can be configured.
const (
	KindNtfy    = "ntfy"
	KindGotify  = "gotify"
	KindDiscord = "discord"
)

// ChannelConfig is everything one channel needs to send. Token and WebhookURL
// are secrets; URL and Topic are shown in settings.
type ChannelConfig struct {
	URL        string `json:"url,omitempty"`
	Topic      string `json:"topic,omitempty"`
	Token      string `json:"token,omitempty"`
	WebhookURL string `json:"webhookUrl,omitempty"`
}

// EnvChannel is a channel defined by environment variables, with the stable ID
// its delivery history is kept under.
type EnvChannel struct {
	ID, Kind string
	Config   ChannelConfig
}

// ValidKind reports whether kind can be configured.
func ValidKind(kind string) bool {
	return kind == KindNtfy || kind == KindGotify || kind == KindDiscord
}

// ValidateChannel checks a configuration and says which field is wrong, in
// words fit for the settings form.
func ValidateChannel(kind string, c ChannelConfig) error {
	switch kind {
	case KindNtfy:
		if _, _, err := ntfyEndpoint(c.URL); err != nil {
			return errors.New("Server URL must be an http:// or https:// address without a user name, query or fragment")
		}
		if !ntfyTopicPattern.MatchString(c.Topic) {
			return errors.New("Topic must be 1-64 letters, digits, '-' or '_'")
		}
		if c.Token != "" && !validGotifyToken(c.Token) {
			return errors.New("Access token must be printable text without spaces")
		}
	case KindGotify:
		if _, err := gotifyEndpoint(c.URL); err != nil {
			return errors.New("Server URL must be an http:// or https:// address without a user name, query or fragment")
		}
		if !validGotifyToken(c.Token) {
			return errors.New("Application token is required and must be printable text without spaces")
		}
	case KindDiscord:
		if _, err := discordEndpoint(c.WebhookURL); err != nil {
			return errors.New("Webhook URL must be a https://discord.com/api/webhooks/... address")
		}
	default:
		return errors.New("Unknown channel kind")
	}
	return nil
}

// NewTarget builds a sender for a stored channel. id identifies the channel's
// delivery history and never appears in errors.
func NewTarget(id, kind string, c ChannelConfig) (Target, error) {
	if err := ValidateChannel(kind, c); err != nil {
		return Target{}, err
	}
	var t Target
	var err error
	switch kind {
	case KindNtfy:
		t, err = newNtfyTarget(c.URL, c.Topic, c.Token, newHTTPClient())
	case KindGotify:
		t, err = newGotifyTarget(c.URL, c.Token, newHTTPClient())
	case KindDiscord:
		t, err = newDiscordTarget(c.WebhookURL, newHTTPClient())
	}
	if err != nil {
		return Target{}, err
	}
	t.ID = id
	return t, nil
}

// Describe is the non-secret summary shown for a channel, such as
// "ntfy.sh · topic alerts". It never includes tokens or webhook paths.
func Describe(kind string, c ChannelConfig) string {
	t, err := NewTarget(kind, kind, c)
	if err != nil {
		return ""
	}
	return t.Display
}

// EnvChannels reads the channels defined by environment variables. Partial
// settings are an error, as before, so a typo never silently drops alerts.
func EnvChannels(getenv func(string) string) ([]EnvChannel, error) {
	out := []EnvChannel{}
	ntfy := ChannelConfig{URL: strings.TrimSpace(getenv(NtfyURLVar)), Topic: strings.TrimSpace(getenv(NtfyTopicVar)), Token: getenv(NtfyTokenVar)}
	if ntfy != (ChannelConfig{}) {
		if ntfy.URL == "" || ntfy.Topic == "" {
			return nil, errors.New("ntfy requires both VIGIRA_NTFY_URL and VIGIRA_NTFY_TOPIC")
		}
		if err := ValidateChannel(KindNtfy, ntfy); err != nil {
			return nil, errors.New("VIGIRA_NTFY_*: " + err.Error())
		}
		out = append(out, EnvChannel{ID: NtfyTargetID, Kind: KindNtfy, Config: ntfy})
	}
	gotify := ChannelConfig{URL: strings.TrimSpace(getenv(GotifyURLVar)), Token: getenv(GotifyTokenVar)}
	if gotify != (ChannelConfig{}) {
		if gotify.URL == "" || gotify.Token == "" {
			return nil, errors.New("Gotify requires both VIGIRA_GOTIFY_URL and VIGIRA_GOTIFY_TOKEN")
		}
		if err := ValidateChannel(KindGotify, gotify); err != nil {
			return nil, errors.New("VIGIRA_GOTIFY_*: " + err.Error())
		}
		out = append(out, EnvChannel{ID: GotifyTargetID, Kind: KindGotify, Config: gotify})
	}
	if discord := strings.TrimSpace(getenv(DiscordWebhookURLVar)); discord != "" {
		c := ChannelConfig{WebhookURL: discord}
		if err := ValidateChannel(KindDiscord, c); err != nil {
			return nil, errors.New("VIGIRA_DISCORD_WEBHOOK_URL: " + err.Error())
		}
		out = append(out, EnvChannel{ID: DiscordTargetID, Kind: KindDiscord, Config: c})
	}
	return out, nil
}
