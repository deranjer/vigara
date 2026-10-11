package notifytransport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

func TestFromEnvOptInAndPartialConfiguration(t *testing.T) {
	getenv := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if targets, err := fromEnv(getenv(nil)); err != nil || len(targets) != 0 {
		t.Fatalf("empty environment: targets=%v err=%v", targets, err)
	}
	targets, err := fromEnv(getenv(map[string]string{
		GotifyURLVar: "http://localhost:8008", GotifyTokenVar: "app-token",
		DiscordWebhookURLVar: "https://discord.com/api/webhooks/123/secret",
	}))
	if err != nil || len(targets) != 2 || targets[0].ID != GotifyTargetID || targets[1].ID != DiscordTargetID {
		t.Fatalf("configured environment: targets=%v err=%v", targets, err)
	}
	if _, err := fromEnv(getenv(map[string]string{GotifyURLVar: "http://localhost"})); err == nil || strings.Contains(err.Error(), "localhost") {
		t.Fatalf("partial Gotify config should fail without exposing values: %v", err)
	}
}

func TestGotifyTokenWithCRLFFailsStartup(t *testing.T) {
	_, err := fromEnv(func(key string) string {
		switch key {
		case GotifyURLVar:
			return "http://localhost:8008"
		case GotifyTokenVar:
			return "bad\r\ntoken"
		default:
			return ""
		}
	})
	if err == nil || strings.Contains(err.Error(), "bad`r`ntoken") {
		t.Fatalf("invalid token should fail startup without exposing credential: %v", err)
	}
}
func TestEndpointValidation(t *testing.T) {
	gotify, err := gotifyEndpoint("http://127.0.0.1:8008/gotify/")
	if err != nil || gotify != "http://127.0.0.1:8008/gotify/message" {
		t.Fatalf("gotify endpoint=%q err=%v", gotify, err)
	}
	for _, value := range []string{
		"http://user:secret@example.test", "http://example.test?token=secret",
		"https://example.test/#fragment", "ftp://example.test", "http://example.test/a/../b",
	} {
		if _, err := gotifyEndpoint(value); err == nil {
			t.Errorf("gotify URL %q should be rejected", value)
		}
	}
	if !validGotifyToken("app-token") || validGotifyToken("bad\r\ntoken") {
		t.Fatal("Gotify token validation failed")
	}

	endpoint, err := discordEndpoint("https://discord.com/api/webhooks/123/abc_DEF-9")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Query().Get("wait") != "true" {
		t.Fatalf("Discord endpoint=%q err=%v", endpoint, err)
	}
	for _, value := range []string{
		"http://discord.com/api/webhooks/123/token",
		"https://discordapp.com/api/webhooks/123/token",
		"https://evil.example/api/webhooks/123/token",
		"https://user@discord.com/api/webhooks/123/token",
		"https://discord.com:443/api/webhooks/123/token",
		"https://discord.com/api/webhooks/123/token?wait=false",
		"https://discord.com/api/webhooks/123/token#fragment",
		"https://discord.com/api/webhooks/nope/token",
		"https://discord.com/api/webhooks/123/tok%2Fen",
	} {
		if _, err := discordEndpoint(value); err == nil {
			t.Errorf("Discord URL %q should be rejected", value)
		}
	}
}

func TestGotifyRequestUsesApplicationTokenHeader(t *testing.T) {
	const token = "test-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/message" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("X-Gotify-Key"); got != token {
			t.Errorf("Gotify token header=%q", got)
		}
		if r.URL.Query().Has("token") {
			t.Error("token must not appear in URL query")
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("Content-Type=%q", got)
		}
		var payload gotifyPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if payload.Priority != 5 || payload.Title != "Vigira: firing" || !strings.Contains(payload.Message, "disk usage above threshold") || !strings.Contains(payload.Message, "event-42") {
			t.Errorf("unexpected payload: %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1}`)
	}))
	defer server.Close()
	target, err := newGotifyTarget(server.URL+"/prefix/", token, newHTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Send(context.Background(), sampleMessage()); err != nil {
		t.Fatal(err)
	}
}

func TestRedirectDoesNotForwardGotifyToken(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/collect", http.StatusFound)
	}))
	defer source.Close()
	target, err := newGotifyTarget(source.URL, "secret-token", newHTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	err = target.Send(context.Background(), sampleMessage())
	var delivery *DeliveryError
	if !errorsAs(err, &delivery) || delivery.Temporary() || delivery.Error() == "" {
		t.Fatalf("redirect should be a permanent unexpected status, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") || redirected.Load() != 0 {
		t.Fatalf("redirect leaked token or was followed: err=%v destination requests=%d", err, redirected.Load())
	}
}

func TestDiscordRequestDisablesMentionsAndRespectsContentLimit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/webhooks/123/abc_DEF-9" || r.URL.Query().Get("wait") != "true" {
			t.Errorf("unexpected Discord request: %s %s", r.Method, r.URL.String())
		}
		var payload discordPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if len(payload.AllowedMentions.Parse) != 0 || payload.AllowedMentions.Parse == nil {
			t.Errorf("allowed_mentions.parse=%v", payload.AllowedMentions.Parse)
		}
		if !strings.Contains(payload.Content, "event-42") || !strings.Contains(payload.Content, "Summary: ") || !strings.Contains(payload.Content, "Occurred: 2026-10-11T12:30:00Z") {
			t.Errorf("message omitted event details: %q", payload.Content)
		}
		if !strings.Contains(payload.Content, "@\u200beveryone") || !strings.Contains(payload.Content, `\*disk\*`) {
			t.Errorf("untrusted names were not escaped: %q", payload.Content)
		}
		units := 0
		for _, r := range payload.Content {
			units += utf16.RuneLen(r)
		}
		if units > maxDiscordContentUnits {
			t.Errorf("content has %d UTF-16 units", units)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"123456789012345678"}`)
	}))
	defer server.Close()
	transport := &rewriteTransport{inner: server.Client().Transport, host: server.Listener.Addr().String()}
	client := newHTTPClient()
	client.Transport = transport
	target, err := newDiscordTarget("https://discord.com/api/webhooks/123/abc_DEF-9", client)
	if err != nil {
		t.Fatal(err)
	}
	message := sampleMessage()
	message.DeviceName = "@everyone *disk*"
	message.Summary = strings.Repeat("📈 @everyone **high** ", 1000)
	if err := target.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
}

func TestStatusRetryClassificationAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body       string
		header     string
		temporary  bool
		retryAfter time.Duration
		code       string
	}{
		{status: http.StatusBadRequest, body: "private rejection body", temporary: false, code: "rejected"},
		{status: http.StatusTooManyRequests, body: `{"retry_after":2.5}`, temporary: true, retryAfter: 2500 * time.Millisecond, code: "rate_limited"},
		{status: http.StatusTooManyRequests, body: `{"retry_after":2.5}`, header: "9999999", temporary: true, retryAfter: maxRetryAfter, code: "rate_limited"},
		{status: http.StatusTooManyRequests, body: `{"retry_after":2.5}`, header: "NaN", temporary: true, retryAfter: 2500 * time.Millisecond, code: "rate_limited"},
		{status: http.StatusTooManyRequests, body: `{"retry_after":2.5}`, header: "Inf", temporary: true, retryAfter: 2500 * time.Millisecond, code: "rate_limited"},
		{status: http.StatusTooManyRequests, body: `{"retry_after":2.5}`, header: "-1", temporary: true, retryAfter: 2500 * time.Millisecond, code: "rate_limited"},
		{status: http.StatusTooManyRequests, body: `{"retry_after":1e100}`, temporary: true, retryAfter: maxRetryAfter, code: "rate_limited"},
		{status: http.StatusTooManyRequests, body: `{"retry_after":-1}`, temporary: true, code: "rate_limited"},
		{status: http.StatusServiceUnavailable, body: "private server body", temporary: true, code: "server_error"},
		{status: http.StatusFound, body: "redirect", temporary: false, code: "unexpected_status"},
	} {
		t.Run(http.StatusText(tc.status)+"/"+tc.header+tc.body, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{tc.header}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			target, err := newDiscordTarget("https://discord.com/api/webhooks/123/secret-token", client)
			if err != nil {
				t.Fatal(err)
			}
			err = target.Send(context.Background(), sampleMessage())
			if err == nil {
				t.Fatal("expected delivery error")
			}
			var delivery *DeliveryError
			if !errorsAs(err, &delivery) || delivery.Temporary() != tc.temporary || delivery.RetryAfter() != tc.retryAfter || delivery.ErrorCode() != tc.code {
				t.Fatalf("classification=%#v, want temporary=%v retryAfter=%v code=%q", delivery, tc.temporary, tc.retryAfter, tc.code)
			}
			if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), tc.body) || strings.Contains(err.Error(), "discord.com") {
				t.Fatalf("error leaked request/response details: %v", err)
			}
		})
	}
}
func TestProviderAcknowledgementsValidateResponse(t *testing.T) {
	for _, provider := range []string{"gotify", "discord"} {
		for _, tc := range []struct {
			name    string
			body    string
			broken  bool
			tooLong bool
			valid   bool
		}{
			{name: "valid", body: `{"id":1}`, valid: true},
			{name: "html login", body: "<html>login</html>"},
			{name: "missing id", body: `{"message":"accepted"}`},
			{name: "truncated", body: `{"id":`, broken: true},
			{name: "oversized", body: strings.Repeat("x", maxResponseBody+1), tooLong: true},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				body := tc.body
				if provider == "discord" && tc.name == "valid" {
					body = `{"id":"123456789012345678"}`
				}
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					var responseBody io.ReadCloser = io.NopCloser(strings.NewReader(body))
					if tc.broken {
						responseBody = &brokenResponseBody{}
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: responseBody}, nil
				})}
				var target Target
				var err error
				if provider == "gotify" {
					target, err = newGotifyTarget("http://gotify.local", "app-token", client)
				} else {
					target, err = newDiscordTarget("https://discord.com/api/webhooks/123/token", client)
				}
				if err != nil {
					t.Fatal(err)
				}
				err = target.Send(context.Background(), sampleMessage())
				if tc.valid {
					if err != nil {
						t.Fatalf("valid acknowledgement rejected: %v", err)
					}
					return
				}
				var delivery *DeliveryError
				if !errorsAs(err, &delivery) || !delivery.Temporary() || delivery.reason != "response_failed" {
					t.Fatalf("invalid success acknowledgement should be retryable response_failed: %v", err)
				}
			})
		}
	}
}

func TestResponseBodyReadIsBounded(t *testing.T) {
	body := &countingBody{reader: strings.NewReader(strings.Repeat("x", maxResponseBody+maxResponseDrain+1000))}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}
	target, err := newGotifyTarget("http://gotify.local", "app-token", client)
	if err != nil {
		t.Fatal(err)
	}
	err = target.Send(context.Background(), sampleMessage())
	var delivery *DeliveryError
	if !errorsAs(err, &delivery) || !delivery.Temporary() || delivery.reason != "response_failed" {
		t.Fatalf("expected retryable bounded-response failure, got %v", err)
	}
	if body.readBytes > maxResponseBody+1+maxResponseDrain {
		t.Fatalf("read %d response bytes, bound is %d", body.readBytes, maxResponseBody+1+maxResponseDrain)
	}
}

func TestGotifyPayloadStaysBoundedAndEscaped(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(io.LimitReader(request.Body, maxGotifyPayloadBytes+1))
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		if len(body) > maxGotifyPayloadBytes {
			t.Errorf("Gotify payload is %d bytes", len(body))
		}
		var payload gotifyPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if len(payload.Message) > maxGotifyMessageBytes {
			t.Errorf("Gotify message is %d bytes", len(payload.Message))
		}
		if !strings.Contains(payload.Message, `\<b\>\*\*notice\*\*`) || !strings.Contains(payload.Message, "@\u200beveryone") {
			t.Errorf("Gotify message did not escape untrusted text: %q", payload.Message)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":7}`))}, nil
	})}
	target, err := newGotifyTarget("http://gotify.local", "app-token", client)
	if err != nil {
		t.Fatal(err)
	}
	message := sampleMessage()
	message.Transition = strings.Repeat("*", 1000)
	message.DeviceName = "<b>**notice** @everyone"
	message.RuleName = strings.Repeat("\\", 10000)
	message.Summary = strings.Repeat("📦 *load* @everyone ", 10000)
	if err := target.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
}
func TestDiscordPayloadStaysWithinUTF16Limit(t *testing.T) {
	message := Message{
		EventID: strings.Repeat("🚨*", 1000), Transition: strings.Repeat("recovery", 100),
		DeviceName: strings.Repeat("📦", 1000), RuleName: strings.Repeat("[rule]", 1000),
		Summary: strings.Repeat("high load @everyone", 1000), OccurredAt: time.Now(),
	}
	content := renderDiscord(message)
	units := 0
	for _, r := range content {
		units += utf16.RuneLen(r)
	}
	if units > maxDiscordContentUnits {
		t.Fatalf("content has %d UTF-16 units", units)
	}
}

func sampleMessage() Message {
	return Message{EventID: "event-42", Transition: "firing", DeviceName: "disk", RuleName: "disk-usage", Summary: "disk usage above threshold", OccurredAt: time.Date(2026, 10, 11, 12, 30, 0, 0, time.UTC)}
}

type rewriteTransport struct {
	inner http.RoundTripper
	host  string
}

func (t *rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	urlCopy := *request.URL
	urlCopy.Host = t.host
	clone.URL = &urlCopy
	return t.inner.RoundTrip(clone)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

type brokenResponseBody struct{ read bool }

func (b *brokenResponseBody) Read(buffer []byte) (int, error) {
	if b.read {
		return 0, io.ErrUnexpectedEOF
	}
	b.read = true
	return copy(buffer, `{"id":`), nil
}
func (*brokenResponseBody) Close() error { return nil }

type countingBody struct {
	reader    *strings.Reader
	readBytes int
}

func (b *countingBody) Read(buffer []byte) (int, error) {
	n, err := b.reader.Read(buffer)
	b.readBytes += n
	return n, err
}
func (*countingBody) Close() error { return nil }

func errorsAs(err error, target any) bool {
	if pointer, ok := target.(**DeliveryError); ok {
		delivery, ok := err.(*DeliveryError)
		if ok {
			*pointer = delivery
		}
		return ok
	}
	return false
}
