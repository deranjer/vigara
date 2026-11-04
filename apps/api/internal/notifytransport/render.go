package notifytransport

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const (
	maxDiscordContentUnits = 2000
	maxGotifyMessageBytes  = 4096
)

func renderDiscord(message Message) string {
	// Field caps leave room for labels, event metadata, escaping, and a useful
	// summary while keeping Discord's UTF-16 content limit exact.
	transition := escapeMarkdown(cleanField(message.Transition, 40))
	eventID := escapeMarkdown(cleanField(message.EventID, 100))
	device := escapeMarkdown(cleanField(message.DeviceName, 100))
	rule := escapeMarkdown(cleanField(message.RuleName, 100))
	summary := escapeMarkdown(cleanField(message.Summary, 450))
	occurred := timestamp(message.OccurredAt)

	lines := []string{
		"Vigara device alert",
		"Transition: " + fallback(transition),
		"Event: " + fallback(eventID),
		"Occurred: " + occurred,
		"Device: " + fallback(device),
		"Rule: " + fallback(rule),
		"Summary: " + fallback(summary),
	}
	return truncateUTF16(strings.Join(lines, "\n"), maxDiscordContentUnits)
}

func renderGotify(message Message) (title, content string) {
	transition := cleanField(message.Transition, 80)
	title = truncateUTF8("Vigara: "+fallback(escapeMarkdown(transition)), 128)
	lines := []string{
		"Transition: " + fallback(escapeMarkdown(transition)),
		"Event: " + fallback(escapeMarkdown(cleanField(message.EventID, 240))),
		"Occurred: " + timestamp(message.OccurredAt),
		"Device: " + fallback(escapeMarkdown(cleanField(message.DeviceName, 400))),
		"Rule: " + fallback(escapeMarkdown(cleanField(message.RuleName, 400))),
		"Summary: " + fallback(escapeMarkdown(cleanField(message.Summary, 2400))),
	}
	content = truncateUTF8(strings.Join(lines, "\n"), maxGotifyMessageBytes)
	return title, content
}
func timestamp(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}

func cleanField(value string, maxRunes int) string {
	var builder strings.Builder
	builder.Grow(min(len(value), maxRunes))
	count := 0
	for _, r := range value {
		if count >= maxRunes {
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		builder.WriteRune(r)
		count++
	}
	return strings.TrimSpace(strings.Join(strings.Fields(builder.String()), " "))
}

func escapeMarkdown(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if r == '@' {
			builder.WriteString("@\u200b")
			continue
		}
		switch r {
		case '\\', '`', '*', '_', '~', '|', '<', '>', '#', '[', ']', '(', ')':
			builder.WriteRune('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
func fallback(value string) string {
	if value == "" {
		return "(unknown)"
	}
	return value
}

func truncateUTF16(value string, maxUnits int) string {
	if maxUnits <= 0 {
		return ""
	}
	units := 0
	var builder strings.Builder
	for _, r := range value {
		runeUnits := utf16.RuneLen(r)
		if runeUnits < 0 || units+runeUnits > maxUnits {
			break
		}
		builder.WriteRune(r)
		units += runeUnits
	}
	return builder.String()
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		n := len(string(r))
		if builder.Len()+n > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
