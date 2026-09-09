package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type quotaTestHost struct {
	mu        sync.Mutex
	files     []AuthFile
	auth      map[string]json.RawMessage
	responses map[string]HostHTTPResponse
	errors    map[string]error
	requests  []HostHTTPRequest
}

func (h *quotaTestHost) ListAuthFiles(context.Context) ([]AuthFile, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]AuthFile(nil), h.files...), nil
}

func (h *quotaTestHost) GetAuth(_ context.Context, authIndex string) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, ok := h.auth[authIndex]
	if !ok {
		return nil, errors.New("not found")
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (h *quotaTestHost) HTTPDo(_ context.Context, request HostHTTPRequest) (HostHTTPResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, request)
	if err := h.errors[request.URL]; err != nil {
		return HostHTTPResponse{}, err
	}
	response, ok := h.responses[request.URL]
	if !ok {
		return HostHTTPResponse{StatusCode: 404, Body: []byte(`{"error":"missing test response"}`)}, nil
	}
	response.Body = append([]byte(nil), response.Body...)
	return response, nil
}

func (h *quotaTestHost) Log(context.Context, string, string, map[string]any) {}

func quotaUsageFixture() []byte {
	return []byte(`{
		"rate_limit": {
			"primary_window": {"used_percent": 18, "limit_window_seconds": 18000, "reset_at": 1788951000},
			"secondary_window": {"used_percent": 47, "limit_window_seconds": 604800, "reset_at": 1789555800}
		},
		"rate_limit_reset_credits": {"available_count": 2, "applicable_available_count": 2}
	}`)
}

func quotaResetFixture() []byte {
	return []byte(`{
		"available_count": 2,
		"applicable_available_count": 2,
		"credits": [
			{"id":"second","reset_type":"codex_rate_limits","status":"available","expires_at":"2026-09-19T00:00:00Z"},
			{"id":"ignored","reset_type":"codex_rate_limits","status":"consumed","expires_at":"2026-09-17T00:00:00Z"},
			{"id":"first","reset_type":"codex_rate_limits","status":"available","expires_at":"2026-09-16T00:00:00Z"}
		]
	}`)
}

func TestQuotaRefreshCachesSnapshotAndKeepsLastSuccessOnFailure(t *testing.T) {
	host := &quotaTestHost{
		files: []AuthFile{{AuthIndex: "idx-1", Type: "codex", Email: "one@example.com"}},
		auth: map[string]json.RawMessage{
			"idx-1": json.RawMessage(`{"access_token":"secret","account_id":"acct-1","email":"one@example.com"}`),
		},
		responses: map[string]HostHTTPResponse{
			codexQuotaUsageURL:        {StatusCode: 200, Body: quotaUsageFixture()},
			codexQuotaResetCreditsURL: {StatusCode: 200, Body: quotaResetFixture()},
		},
		errors: map[string]error{},
	}
	runtime := NewRuntime(host, t.TempDir())
	t.Cleanup(runtime.Stop)

	first := runtime.RefreshQuota("idx-1")
	if first.Status != "success" {
		t.Fatalf("status = %q, error = %q", first.Status, first.Error)
	}
	if len(first.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(first.Windows))
	}
	if got := int(first.Windows[0].Minutes + 0.5); got != 300 {
		t.Fatalf("first window = %d minutes, want 300", got)
	}
	if got := int(first.Windows[1].Minutes + 0.5); got != 10080 {
		t.Fatalf("second window = %d minutes, want 10080", got)
	}
	if len(first.ResetCredits) != 2 || first.ResetCredits[0].ID != "first" || first.ResetCredits[1].ID != "second" {
		t.Fatalf("reset credits not sorted/filtered: %+v", first.ResetCredits)
	}
	if first.ResetApplicableCount == nil || *first.ResetApplicableCount != 2 {
		t.Fatalf("reset applicable count = %v, want 2", first.ResetApplicableCount)
	}

	host.mu.Lock()
	host.responses[codexQuotaUsageURL] = HostHTTPResponse{StatusCode: 500, Body: []byte(`{"error":{"message":"temporary"}}`)}
	host.mu.Unlock()
	second := runtime.RefreshQuota("idx-1")
	if second.Status != "error" {
		t.Fatalf("status after failed refresh = %q, want error", second.Status)
	}
	if len(second.Windows) != 2 || second.FetchedAt.IsZero() {
		t.Fatalf("failed refresh should retain last successful snapshot: %+v", second)
	}
	if !second.FetchedAt.Equal(first.FetchedAt) {
		t.Fatalf("fetched_at changed on failed refresh: %s -> %s", first.FetchedAt, second.FetchedAt)
	}

	view := runtime.QuotaCache()
	if len(view.Quotas) != 1 || view.Quotas[0].AuthIndex != "idx-1" {
		t.Fatalf("unexpected cache view: %+v", view)
	}
}

func TestQuotaRefreshAllCompletesWithPerAccountFailures(t *testing.T) {
	host := &quotaTestHost{
		files: []AuthFile{
			{AuthIndex: "idx-1", Type: "codex", Email: "one@example.com"},
			{AuthIndex: "idx-2", Type: "codex", Email: "two@example.com"},
		},
		auth: map[string]json.RawMessage{
			"idx-1": json.RawMessage(`{"access_token":"secret","account_id":"acct-1"}`),
		},
		responses: map[string]HostHTTPResponse{
			codexQuotaUsageURL:        {StatusCode: 200, Body: quotaUsageFixture()},
			codexQuotaResetCreditsURL: {StatusCode: 200, Body: quotaResetFixture()},
		},
		errors: map[string]error{},
	}
	runtime := NewRuntime(host, t.TempDir())
	t.Cleanup(runtime.Stop)

	view := runtime.RefreshAllQuotas()
	if view.RefreshAll.Status != "completed" {
		t.Fatalf("refresh-all status = %q", view.RefreshAll.Status)
	}
	if view.RefreshAll.Total != 2 || view.RefreshAll.Completed != 2 || view.RefreshAll.Success != 1 || view.RefreshAll.Failed != 1 {
		t.Fatalf("unexpected refresh-all summary: %+v", view.RefreshAll)
	}
	if len(view.Quotas) != 2 {
		t.Fatalf("quota cache entries = %d, want 2", len(view.Quotas))
	}
	for _, item := range view.Quotas {
		if item.Status == "refreshing" {
			t.Fatalf("account left refreshing: %+v", item)
		}
	}
}

func TestQuotaParserSupportsResetAfterAndNumericTimeStrings(t *testing.T) {
	fetchedAt := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": float64(25), "limit_window_seconds": float64(18000), "reset_after_seconds": float64(300)},
			"secondary_window": map[string]any{"used_percent": float64(50), "limit_window_seconds": float64(604800), "reset_at": "1789555800"},
		},
	}
	windows, err := parseCodexQuotaWindows(payload, fetchedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got := windows[0].ResetAt.Sub(fetchedAt); got != 5*time.Minute {
		t.Fatalf("reset_after delta = %s, want 5m", got)
	}
	if windows[1].ResetAt.IsZero() {
		t.Fatal("numeric reset_at string should be parsed")
	}
}
