package notifytransport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func postJSON(client *http.Client, request *http.Request, target string, validateSuccess func([]byte) bool) error {
	response, err := client.Do(request)
	if err != nil {
		return &DeliveryError{target: target, reason: "request_failed", temporary: true}
	}
	body, tooLarge, readErr := consumeResponse(response)
	if response.StatusCode == http.StatusOK {
		if readErr != nil || tooLarge || !validateSuccess(body) {
			return &DeliveryError{target: target, reason: "response_failed", temporary: true}
		}
		return nil
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return &DeliveryError{target: target, reason: "response_failed", temporary: true}
	}

	temporary := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	reason := "rejected"
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		reason = "rate_limited"
	case response.StatusCode >= 500:
		reason = "server_error"
	case response.StatusCode < 400:
		reason = "unexpected_status"
	}
	result := &DeliveryError{target: target, reason: reason, temporary: temporary}
	if response.StatusCode == http.StatusTooManyRequests {
		result.retryAfter = retryAfter(response.Header, body)
	}
	return result
}

func consumeResponse(response *http.Response) ([]byte, bool, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	tooLarge := len(body) > maxResponseBody
	if tooLarge {
		body = body[:maxResponseBody]
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseDrain))
	}
	return body, tooLarge, err
}

func retryAfter(header http.Header, body []byte) time.Duration {
	if delay := parseRetryAfterValue(header.Get("Retry-After")); delay > 0 {
		return delay
	}
	var value struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if len(body) == 0 || json.Unmarshal(body, &value) != nil {
		return 0
	}
	return secondsDuration(value.RetryAfter)
}

func parseRetryAfterValue(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if parsed, err := strconv.ParseFloat(value, 64); err == nil {
		return secondsDuration(parsed)
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return clampRetryAfter(time.Until(parsed))
	}
	return 0
}

func secondsDuration(seconds float64) time.Duration {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0
	}
	if seconds >= maxRetryAfter.Seconds() {
		return maxRetryAfter
	}
	return time.Duration(seconds * float64(time.Second))
}

func clampRetryAfter(value time.Duration) time.Duration {
	if value <= 0 {
		return 0
	}
	if value > maxRetryAfter {
		return maxRetryAfter
	}
	return value
}

func newJSONRequest(ctx context.Context, endpoint string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}
