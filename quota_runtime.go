package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	codexQuotaUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexQuotaResetConsumeURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"

	quotaAccountTimeout     = 40 * time.Second
	quotaHostTimeout        = 8 * time.Second
	quotaUsageTimeout       = 20 * time.Second
	quotaResetInfoTimeout   = 12 * time.Second
	quotaResetActionTimeout = 20 * time.Second
	quotaRefreshConcurrency = 6
)

type quotaWindow struct {
	Minutes   float64   `json:"minutes"`
	Used      float64   `json:"used"`
	Remaining float64   `json:"remaining"`
	ResetAt   time.Time `json:"reset_at,omitempty"`
}

type quotaResetCredit struct {
	ID        string    `json:"id,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type quotaSnapshot struct {
	AuthIndex            string             `json:"auth_index"`
	Status               string             `json:"status"`
	FetchedAt            time.Time          `json:"fetched_at,omitempty"`
	LastAttemptAt        time.Time          `json:"last_attempt_at,omitempty"`
	Windows              []quotaWindow      `json:"windows,omitempty"`
	ResetCredits         []quotaResetCredit `json:"reset_credits,omitempty"`
	ResetAvailableCount  *int               `json:"reset_available_count,omitempty"`
	ResetApplicableCount *int               `json:"reset_applicable_count,omitempty"`
	ResetError           string             `json:"reset_error,omitempty"`
	Error                string             `json:"error,omitempty"`
}

type quotaRefreshAllState struct {
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Completed  int       `json:"completed"`
	Success    int       `json:"success"`
	Failed     int       `json:"failed"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type quotaCacheView struct {
	Quotas     []quotaSnapshot      `json:"quotas"`
	RefreshAll quotaRefreshAllState `json:"refresh_all"`
}

type quotaCacheState struct {
	mu         sync.RWMutex
	snapshots  map[string]quotaSnapshot
	inFlight   map[string]chan struct{}
	refreshAll quotaRefreshAllState
}

var quotaCacheRegistry struct {
	sync.Mutex
	runtime *Runtime
	state   *quotaCacheState
}

func handleQuotaManagement(req managementRequest, path string, rt *Runtime) (managementResponse, bool) {
	switch {
	case req.Method == http.MethodGet && path == "/quota":
		return jsonResponse(http.StatusOK, rt.QuotaCache()), true
	case req.Method == http.MethodPost && path == "/quota/refresh":
		authIndex, err := quotaAuthIndexFromBody(req.Body)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), true
		}
		return jsonResponse(http.StatusOK, map[string]any{"snapshot": rt.RefreshQuota(authIndex)}), true
	case req.Method == http.MethodPost && path == "/quota/refresh-all":
		return jsonResponse(http.StatusOK, rt.RefreshAllQuotas()), true
	case req.Method == http.MethodPost && path == "/quota/reset":
		authIndex, err := quotaAuthIndexFromBody(req.Body)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), true
		}
		return jsonResponse(http.StatusOK, map[string]any{"snapshot": rt.ResetQuota(authIndex)}), true
	default:
		return managementResponse{}, false
	}
}

func quotaAuthIndexFromBody(body []byte) (string, error) {
	var input struct {
		AuthIndex string `json:"auth_index"`
	}
	if len(body) == 0 || json.Unmarshal(body, &input) != nil {
		return "", errors.New("invalid quota request")
	}
	input.AuthIndex = strings.TrimSpace(input.AuthIndex)
	if input.AuthIndex == "" {
		return "", errors.New("auth_index is required")
	}
	return input.AuthIndex, nil
}

func quotaCacheFor(r *Runtime) *quotaCacheState {
	quotaCacheRegistry.Lock()
	defer quotaCacheRegistry.Unlock()
	if quotaCacheRegistry.runtime != r || quotaCacheRegistry.state == nil {
		quotaCacheRegistry.runtime = r
		quotaCacheRegistry.state = &quotaCacheState{
			snapshots: make(map[string]quotaSnapshot),
			inFlight:  make(map[string]chan struct{}),
			refreshAll: quotaRefreshAllState{Status: "idle"},
		}
	}
	return quotaCacheRegistry.state
}

func (s *quotaCacheState) view() quotaCacheView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]quotaSnapshot, 0, len(s.snapshots))
	for _, item := range s.snapshots {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AuthIndex < items[j].AuthIndex })
	return quotaCacheView{Quotas: items, RefreshAll: s.refreshAll}
}

func (s *quotaCacheState) snapshot(authIndex string) quotaSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshots[authIndex]
}

func (s *quotaCacheState) begin(authIndex string) (<-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.inFlight[authIndex]; ok {
		return existing, false
	}
	item := s.snapshots[authIndex]
	item.AuthIndex = authIndex
	item.Status = "refreshing"
	item.LastAttemptAt = time.Now().UTC()
	item.Error = ""
	s.snapshots[authIndex] = item
	ch := make(chan struct{})
	s.inFlight[authIndex] = ch
	return ch, true
}

func (s *quotaCacheState) finish(authIndex string, item quotaSnapshot) quotaSnapshot {
	s.mu.Lock()
	ch := s.inFlight[authIndex]
	delete(s.inFlight, authIndex)
	s.snapshots[authIndex] = item
	s.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	return item
}

func (s *quotaCacheState) finishError(authIndex string, err error) quotaSnapshot {
	s.mu.Lock()
	item := s.snapshots[authIndex]
	item.AuthIndex = authIndex
	item.Status = "error"
	item.LastAttemptAt = time.Now().UTC()
	if err == nil {
		item.Error = "额度刷新失败"
	} else {
		item.Error = err.Error()
	}
	ch := s.inFlight[authIndex]
	delete(s.inFlight, authIndex)
	s.snapshots[authIndex] = item
	s.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	return item
}

func (s *quotaCacheState) startRefreshAll(total int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshAll.Status == "refreshing" {
		return false
	}
	s.refreshAll = quotaRefreshAllState{Status: "refreshing", Total: total, StartedAt: time.Now().UTC()}
	return true
}

func (s *quotaCacheState) addRefreshAllResult(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshAll.Completed++
	if success {
		s.refreshAll.Success++
	} else {
		s.refreshAll.Failed++
	}
}

func (s *quotaCacheState) finishRefreshAll(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshAll.Status = status
	s.refreshAll.FinishedAt = time.Now().UTC()
}

func (r *Runtime) QuotaCache() quotaCacheView {
	return quotaCacheFor(r).view()
}

func (r *Runtime) RefreshQuota(authIndex string) quotaSnapshot {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return quotaSnapshot{Status: "error", Error: "缺少 auth_index"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), quotaAccountTimeout)
	defer cancel()
	account, err := r.quotaFindAccount(ctx, authIndex)
	if err != nil {
		store := quotaCacheFor(r)
		if _, owner := store.begin(authIndex); owner {
			return store.finishError(authIndex, err)
		}
		return store.snapshot(authIndex)
	}
	return r.refreshQuotaAccount(ctx, account)
}

func (r *Runtime) RefreshAllQuotas() quotaCacheView {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	store := quotaCacheFor(r)
	accounts, err := r.quotaListAccounts(ctx)
	if err != nil {
		store.mu.Lock()
		store.refreshAll = quotaRefreshAllState{Status: "error", Failed: 1, FinishedAt: time.Now().UTC()}
		store.mu.Unlock()
		return store.view()
	}
	targets := make([]AuthFile, 0, len(accounts))
	for _, account := range accounts {
		if !account.Disabled {
			targets = append(targets, account)
		}
	}
	if !store.startRefreshAll(len(targets)) {
		return store.view()
	}
	if len(targets) == 0 {
		store.finishRefreshAll("completed")
		return store.view()
	}
	jobs := make(chan AuthFile)
	workerCount := quotaRefreshConcurrency
	if len(targets) < workerCount {
		workerCount = len(targets)
	}
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for account := range jobs {
				accountCtx, accountCancel := context.WithTimeout(ctx, quotaAccountTimeout)
				snapshot := r.refreshQuotaAccount(accountCtx, account)
				accountCancel()
				store.addRefreshAllResult(snapshot.Status == "success")
			}
		}()
	}
	cancelled := false
sendLoop:
	for _, account := range targets {
		select {
		case jobs <- account:
		case <-ctx.Done():
			cancelled = true
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	if cancelled {
		store.finishRefreshAll("error")
	} else {
		store.finishRefreshAll("completed")
	}
	return store.view()
}

func (r *Runtime) ResetQuota(authIndex string) quotaSnapshot {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return quotaSnapshot{Status: "error", Error: "缺少 auth_index"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), quotaAccountTimeout)
	defer cancel()
	account, err := r.quotaFindAccount(ctx, authIndex)
	if err != nil {
		return quotaCacheFor(r).finishError(authIndex, err)
	}
	if account.Disabled {
		return quotaCacheFor(r).finishError(authIndex, errors.New("已停用账号不能重置额度"))
	}
	material, err := r.quotaAuthMaterial(ctx, authIndex)
	if err != nil {
		return quotaCacheFor(r).finishError(authIndex, err)
	}
	body, _ := json.Marshal(map[string]string{"redeem_request_id": quotaRequestID()})
	if _, err := r.quotaHTTPJSON(ctx, http.MethodPost, codexQuotaResetConsumeURL, material, nil, body, quotaResetActionTimeout); err != nil {
		return quotaCacheFor(r).finishError(authIndex, fmt.Errorf("重置额度失败：%w", err))
	}
	return r.refreshQuotaAccount(ctx, account)
}

func (r *Runtime) refreshQuotaAccount(ctx context.Context, account AuthFile) quotaSnapshot {
	authIndex := strings.TrimSpace(account.AuthIndex)
	store := quotaCacheFor(r)
	wait, owner := store.begin(authIndex)
	if !owner {
		select {
		case <-wait:
			return store.snapshot(authIndex)
		case <-ctx.Done():
			return store.snapshot(authIndex)
		}
	}
	if account.Disabled {
		return store.finishError(authIndex, errors.New("已停用账号不刷新额度"))
	}
	material, err := r.quotaAuthMaterial(ctx, authIndex)
	if err != nil {
		return store.finishError(authIndex, err)
	}
	fetchedAt := time.Now().UTC()
	usage, err := r.quotaHTTPJSON(ctx, http.MethodGet, codexQuotaUsageURL, material, nil, nil, quotaUsageTimeout)
	if err != nil {
		return store.finishError(authIndex, err)
	}
	windows, err := parseCodexQuotaWindows(usage, fetchedAt)
	if err != nil {
		return store.finishError(authIndex, err)
	}
	credits := parseCodexResetCredits(firstMapValue(usage, "rate_limit_reset_credits", "rateLimitResetCredits"))
	resetError := ""
	resetHeaders := map[string][]string{
		"Accept":      {"application/json"},
		"OpenAI-Beta": {"codex-1"},
		"Originator":  {"Codex Desktop"},
	}
	if detail, detailErr := r.quotaHTTPJSON(ctx, http.MethodGet, codexQuotaResetCreditsURL, material, resetHeaders, nil, quotaResetInfoTimeout); detailErr == nil {
		parsed := parseCodexResetCredits(detail)
		if parsed.valid {
			credits.credits = parsed.credits
			if parsed.availableCount != nil {
				credits.availableCount = parsed.availableCount
			}
			if parsed.applicableAvailableCount != nil {
				credits.applicableAvailableCount = parsed.applicableAvailableCount
			}
		} else {
			resetError = "重置信息返回格式不可识别"
		}
	} else {
		resetError = detailErr.Error()
	}
	if credits.availableCount == nil && len(credits.credits) > 0 {
		count := len(credits.credits)
		credits.availableCount = &count
	}
	if credits.applicableAvailableCount == nil {
		credits.applicableAvailableCount = credits.availableCount
	}
	item := quotaSnapshot{
		AuthIndex:            authIndex,
		Status:               "success",
		FetchedAt:            fetchedAt,
		LastAttemptAt:        fetchedAt,
		Windows:              windows,
		ResetCredits:         credits.credits,
		ResetAvailableCount:  credits.availableCount,
		ResetApplicableCount: credits.applicableAvailableCount,
		ResetError:           resetError,
	}
	return store.finish(authIndex, item)
}

type quotaCallResult[T any] struct {
	value T
	err   error
}

func quotaCall[T any](parent context.Context, timeout time.Duration, timeoutMessage string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ch := make(chan quotaCallResult[T], 1)
	go func() {
		value, err := fn(ctx)
		ch <- quotaCallResult[T]{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		return zero, errors.New(timeoutMessage)
	case result := <-ch:
		return result.value, result.err
	}
}

func (r *Runtime) quotaFindAccount(ctx context.Context, authIndex string) (AuthFile, error) {
	accounts, err := r.quotaListAccounts(ctx)
	if err != nil {
		return AuthFile{}, err
	}
	for _, account := range accounts {
		if strings.TrimSpace(account.AuthIndex) == authIndex {
			return account, nil
		}
	}
	return AuthFile{}, errors.New("找不到对应的 Codex 凭证")
}

func (r *Runtime) quotaListAccounts(ctx context.Context) ([]AuthFile, error) {
	accounts, err := quotaCall(ctx, quotaHostTimeout, "读取 Codex 凭证超时", func(callCtx context.Context) ([]AuthFile, error) {
		return r.host.ListAuthFiles(callCtx)
	})
	if err != nil {
		if err.Error() == "读取 Codex 凭证超时" {
			return nil, err
		}
		return nil, errors.New("无法读取 Codex 凭证")
	}
	filtered := make([]AuthFile, 0, len(accounts))
	for _, account := range accounts {
		if isCodexAuth(account) {
			filtered = append(filtered, account)
		}
	}
	return filtered, nil
}

func (r *Runtime) quotaAuthMaterial(ctx context.Context, authIndex string) (authMaterial, error) {
	raw, err := quotaCall(ctx, quotaHostTimeout, "读取 Codex 凭证超时", func(callCtx context.Context) (json.RawMessage, error) {
		return r.host.GetAuth(callCtx, authIndex)
	})
	if err != nil {
		if err.Error() == "读取 Codex 凭证超时" {
			return authMaterial{}, err
		}
		return authMaterial{}, errors.New("无法读取 Codex 凭证")
	}
	var material authMaterial
	if json.Unmarshal(raw, &material) != nil || strings.TrimSpace(material.AccessToken) == "" {
		return authMaterial{}, errors.New("Codex 凭证中没有可用的 access token")
	}
	return material, nil
}

func (r *Runtime) quotaHTTPJSON(ctx context.Context, method, url string, material authMaterial, extraHeaders map[string][]string, body []byte, timeout time.Duration) (map[string]any, error) {
	headers := map[string][]string{
		"Authorization": {"Bearer " + strings.TrimSpace(material.AccessToken)},
		"Accept":        {"application/json"},
		"Content-Type":  {"application/json"},
		"User-Agent":    {fmt.Sprintf("codex-tui/0.149.1 (Linux; %s)", runtime.GOARCH)},
	}
	if accountID := strings.TrimSpace(material.AccountID); accountID != "" {
		headers["Chatgpt-Account-Id"] = []string{accountID}
	}
	for key, values := range extraHeaders {
		headers[key] = append([]string(nil), values...)
	}
	request := HostHTTPRequest{Method: method, URL: url, Headers: headers, Body: body}
	response, err := quotaCall(ctx, timeout, "额度接口请求超时", func(callCtx context.Context) (HostHTTPResponse, error) {
		return r.host.HTTPDo(callCtx, request)
	})
	if err != nil {
		if err.Error() == "额度接口请求超时" {
			return nil, err
		}
		return nil, errors.New("额度接口请求失败")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := quotaErrorDetail(response.Body)
		if detail != "" {
			return nil, fmt.Errorf("额度接口 HTTP %d · %s", response.StatusCode, detail)
		}
		return nil, fmt.Errorf("额度接口 HTTP %d", response.StatusCode)
	}
	if len(response.Body) == 0 {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if json.Unmarshal(response.Body, &decoded) != nil {
		return nil, errors.New("额度接口返回了无法解析的数据")
	}
	return decoded, nil
}

func quotaErrorDetail(body []byte) string {
	var value map[string]any
	if json.Unmarshal(body, &value) == nil {
		if nested, ok := value["error"].(map[string]any); ok {
			if message, ok := nested["message"].(string); ok {
				return strings.TrimSpace(message)
			}
		}
		for _, key := range []string{"message", "error"} {
			if message, ok := value[key].(string); ok {
				return strings.TrimSpace(message)
			}
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 180 {
		text = text[:180]
	}
	return text
}

func parseCodexQuotaWindows(payload map[string]any, fetchedAt time.Time) ([]quotaWindow, error) {
	rate := firstMapValue(payload, "rate_limit", "rateLimit")
	primary := firstMapValue(rate, "primary_window", "primaryWindow")
	secondary := firstMapValue(rate, "secondary_window", "secondaryWindow")
	windows := []map[string]any{primary, secondary}
	secondsOf := func(window map[string]any) float64 {
		return numberValue(firstValue(window, "limit_window_seconds", "limitWindowSeconds"))
	}
	isLong := func(window map[string]any) bool {
		seconds := secondsOf(window)
		return seconds == 604800 || (seconds >= 28*86400 && seconds <= 31*86400)
	}
	var fiveHour, longWindow map[string]any
	for _, window := range windows {
		if window == nil {
			continue
		}
		seconds := secondsOf(window)
		if seconds == 18000 && fiveHour == nil {
			fiveHour = window
		}
		if isLong(window) && longWindow == nil {
			longWindow = window
		}
	}
	if fiveHour == nil {
		for _, window := range windows {
			if window != nil && !isLong(window) {
				fiveHour = window
				break
			}
		}
	}
	if longWindow == nil {
		for _, window := range []map[string]any{secondary, primary} {
			if window != nil && secondsOf(window) != 18000 {
				longWindow = window
				break
			}
		}
	}
	limitReached, _ := firstValue(rate, "limit_reached", "limitReached").(bool)
	if allowed, ok := rate["allowed"].(bool); ok && !allowed {
		limitReached = true
	}
	makeWindow := func(window map[string]any, fallbackMinutes float64) (quotaWindow, bool) {
		if window == nil {
			return quotaWindow{}, false
		}
		minutes := fallbackMinutes
		if seconds := secondsOf(window); seconds > 0 {
			minutes = seconds / 60
		}
		used, hasUsed := numberValueOK(firstValue(window, "used_percent", "usedPercent"))
		resetAt := quotaResetAt(window, fetchedAt)
		if !hasUsed && limitReached && !resetAt.IsZero() {
			used, hasUsed = 100, true
		}
		if !hasUsed {
			return quotaWindow{}, false
		}
		if used < 0 {
			used = 0
		}
		if used > 100 {
			used = 100
		}
		return quotaWindow{Minutes: minutes, Used: used, Remaining: 100 - used, ResetAt: resetAt}, true
	}
	result := make([]quotaWindow, 0, 2)
	if item, ok := makeWindow(fiveHour, 300); ok {
		result = append(result, item)
	}
	if item, ok := makeWindow(longWindow, 10080); ok {
		if len(result) == 0 || int(item.Minutes+0.5) != int(result[0].Minutes+0.5) {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("额度接口未返回可识别的 5H / 7D 窗口")
	}
	return result, nil
}

func quotaResetAt(window map[string]any, fetchedAt time.Time) time.Time {
	if parsed := quotaTimeValue(firstValue(window, "reset_at", "resetAt")); !parsed.IsZero() {
		return parsed
	}
	if seconds, ok := numberValueOK(firstValue(window, "reset_after_seconds", "resetAfterSeconds")); ok && seconds >= 0 {
		return fetchedAt.Add(time.Duration(seconds * float64(time.Second))).UTC()
	}
	return time.Time{}
}

type parsedResetCredits struct {
	availableCount           *int
	applicableAvailableCount *int
	credits                  []quotaResetCredit
	valid                    bool
}

func parseCodexResetCredits(value any) parsedResetCredits {
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		return parsedResetCredits{}
	}
	_, creditsKey := record["credits"]
	_, availableSnake := record["available_count"]
	_, availableCamel := record["availableCount"]
	_, applicableSnake := record["applicable_available_count"]
	_, applicableCamel := record["applicableAvailableCount"]
	parsed := parsedResetCredits{valid: creditsKey || availableSnake || availableCamel || applicableSnake || applicableCamel}
	if value, ok := numberValueOK(firstValue(record, "available_count", "availableCount")); ok {
		count := int(value)
		parsed.availableCount = &count
	}
	if value, ok := numberValueOK(firstValue(record, "applicable_available_count", "applicableAvailableCount")); ok {
		count := int(value)
		parsed.applicableAvailableCount = &count
	}
	if list, ok := record["credits"].([]any); ok {
		for _, item := range list {
			credit, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(stringValue(firstValue(credit, "reset_type", "resetType"))) != "codex_rate_limits" || strings.TrimSpace(stringValue(credit["status"])) != "available" {
				continue
			}
			expiresAt := quotaTimeValue(firstValue(credit, "expires_at", "expiresAt"))
			if expiresAt.IsZero() {
				continue
			}
			parsed.credits = append(parsed.credits, quotaResetCredit{ID: stringValue(credit["id"]), ExpiresAt: expiresAt})
		}
	}
	sort.Slice(parsed.credits, func(i, j int) bool { return parsed.credits[i].ExpiresAt.Before(parsed.credits[j].ExpiresAt) })
	return parsed
}

func firstMapValue(record map[string]any, keys ...string) map[string]any {
	if mapped, ok := firstValue(record, keys...).(map[string]any); ok {
		return mapped
	}
	return nil
}

func firstValue(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok {
			return value
		}
	}
	return nil
}

func numberValue(value any) float64 {
	number, _ := numberValueOK(value)
	return number
}

func numberValueOK(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	}
	return 0, false
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func quotaTimeValue(value any) time.Time {
	if value == nil {
		return time.Time{}
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return time.Time{}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UTC()
		}
		if number, err := strconv.ParseFloat(text, 64); err == nil {
			value = number
		}
	}
	number, ok := numberValueOK(value)
	if !ok || number <= 0 {
		return time.Time{}
	}
	if number > 1e12 {
		return time.UnixMilli(int64(number)).UTC()
	}
	return time.Unix(int64(number), 0).UTC()
}

func quotaRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := hex.EncodeToString(raw[:])
	return value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}
