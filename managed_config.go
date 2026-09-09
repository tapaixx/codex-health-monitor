package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	managedStateVersion       = 1
	managedStateFileName      = "window-optimizer.json"
	managedWindowJitterMinute = 3
	managedRetryDelay         = 5 * time.Minute
)

var managedWindowJitter = func() time.Duration {
	return time.Duration(rand.IntN(managedWindowJitterMinute*60+1)) * time.Second
}

type ManagedConfig struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"schedule_mode"`
	IntervalMin     int    `json:"interval_min"`
	DailyTimes      string `json:"daily_times"`
	Timezone        string `json:"timezone"`
	TimeoutSec      int    `json:"timeout_sec"`
	ExcludedEmails  string `json:"excluded_emails,omitempty"`
	Weekdays        string `json:"weekdays,omitempty"`
	WindowHours     int    `json:"window_hours,omitempty"`
	QuotaMinutes    int    `json:"quota_minutes,omitempty"`
	WorkStart       string `json:"work_start,omitempty"`
	LunchStart      string `json:"lunch_start,omitempty"`
	LunchEnd        string `json:"lunch_end,omitempty"`
	WorkEnd         string `json:"work_end,omitempty"`
	AnchorTime      string `json:"anchor_time,omitempty"`
	HealthThreshold int    `json:"health_threshold,omitempty"`
}

type ManagedAccountState struct {
	NextRunAt     time.Time `json:"next_run_at,omitempty"`
	FallbackAt    time.Time `json:"fallback_at,omitempty"`
	RetryPending  bool      `json:"retry_pending,omitempty"`
	RetryTrigger  string    `json:"retry_trigger,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastPlannedAt time.Time `json:"last_planned_at,omitempty"`
}

type managedPersistedState struct {
	Version  int                            `json:"version"`
	Config   ManagedConfig                  `json:"config"`
	Accounts map[string]ManagedAccountState `json:"accounts,omitempty"`
	Planned  map[string]time.Time           `json:"planned,omitempty"`
}

type managedRuntimeState struct {
	once    sync.Once
	loadErr error
	mu      sync.RWMutex
	state   managedPersistedState
}

var managedRuntimeStates sync.Map

func defaultManagedConfig() ManagedConfig {
	return ManagedConfig{
		Enabled:         true,
		Mode:            "window_optimized",
		IntervalMin:     30,
		Timezone:        "Asia/Shanghai",
		TimeoutSec:      30,
		Weekdays:        "1,2,3,4,5",
		WindowHours:     5,
		QuotaMinutes:    60,
		WorkStart:       "09:00",
		LunchStart:      "12:00",
		LunchEnd:        "13:30",
		WorkEnd:         "19:00",
		AnchorTime:      "06:59",
		HealthThreshold: 80,
	}
}

func managedStateFor(r *Runtime) *managedRuntimeState {
	value, _ := managedRuntimeStates.LoadOrStore(r, &managedRuntimeState{})
	state := value.(*managedRuntimeState)
	state.once.Do(func() {
		state.state = managedPersistedState{
			Version:  managedStateVersion,
			Config:   defaultManagedConfig(),
			Accounts: make(map[string]ManagedAccountState),
			Planned:  make(map[string]time.Time),
		}
		raw, err := os.ReadFile(filepath.Join(r.dataDir, managedStateFileName))
		if err != nil {
			if !os.IsNotExist(err) {
				state.loadErr = err
			}
			return
		}
		var persisted managedPersistedState
		if err := json.Unmarshal(raw, &persisted); err != nil {
			state.loadErr = err
			return
		}
		if persisted.Version != managedStateVersion {
			// The first managed version deliberately replaces the legacy scheduler
			// configuration rather than attempting to preserve its cadence.
			return
		}
		normalized, err := normalizeManagedConfig(persisted.Config)
		if err != nil {
			state.loadErr = err
			return
		}
		persisted.Config = normalized
		if persisted.Accounts == nil {
			persisted.Accounts = make(map[string]ManagedAccountState)
		}
		if persisted.Planned == nil {
			persisted.Planned = make(map[string]time.Time)
		}
		state.state = persisted
	})
	return state
}

func cloneManagedAccounts(input map[string]ManagedAccountState) map[string]ManagedAccountState {
	out := make(map[string]ManagedAccountState, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneManagedPlanned(input map[string]time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (m *managedRuntimeState) snapshot() managedPersistedState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return managedPersistedState{
		Version:  m.state.Version,
		Config:   m.state.Config,
		Accounts: cloneManagedAccounts(m.state.Accounts),
		Planned:  cloneManagedPlanned(m.state.Planned),
	}
}

func (m *managedRuntimeState) persist(r *Runtime) {
	snapshot := m.snapshot()
	if err := writeJSONAtomic(filepath.Join(r.dataDir, managedStateFileName), snapshot); err != nil {
		r.host.Log(context.Background(), "error", "window optimizer state could not be persisted", map[string]any{"error_code": "window_state_write_failed"})
	}
}

func normalizeManagedConfig(input ManagedConfig) (ManagedConfig, error) {
	defaults := defaultManagedConfig()
	input.Enabled = true
	input.Mode = strings.TrimSpace(input.Mode)
	if input.Mode == "" {
		input.Mode = defaults.Mode
	}
	if input.Mode != "interval" && input.Mode != "daily_times" && input.Mode != "window_optimized" {
		return ManagedConfig{}, errors.New("schedule_mode must be interval, daily_times, or window_optimized")
	}
	if input.IntervalMin == 0 {
		input.IntervalMin = defaults.IntervalMin
	}
	if input.IntervalMin < 5 || input.IntervalMin > 10080 {
		return ManagedConfig{}, errors.New("interval_min must be between 5 and 10080")
	}
	if strings.TrimSpace(input.Timezone) == "" {
		input.Timezone = defaults.Timezone
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return ManagedConfig{}, errors.New("timezone must be a valid IANA timezone")
	}
	if input.TimeoutSec == 0 {
		input.TimeoutSec = defaults.TimeoutSec
	}
	if input.TimeoutSec < 5 || input.TimeoutSec > 120 {
		return ManagedConfig{}, errors.New("timeout_sec must be between 5 and 120")
	}
	times, err := parseDailyTimes(input.DailyTimes)
	if err != nil {
		return ManagedConfig{}, err
	}
	if input.Mode == "daily_times" && len(times) == 0 {
		return ManagedConfig{}, errors.New("daily_times is required in daily_times mode")
	}
	input.DailyTimes = strings.Join(times, ",")
	input.ExcludedEmails = normalizeManagedSelectionList(input.ExcludedEmails)
	if strings.TrimSpace(input.Weekdays) == "" {
		input.Weekdays = defaults.Weekdays
	}
	weekdays, err := normalizeManagedWeekdays(input.Weekdays)
	if err != nil {
		return ManagedConfig{}, err
	}
	input.Weekdays = weekdays
	if input.WindowHours == 0 {
		input.WindowHours = defaults.WindowHours
	}
	if input.WindowHours < 1 || input.WindowHours > 24 {
		return ManagedConfig{}, errors.New("window_hours must be between 1 and 24")
	}
	if input.QuotaMinutes == 0 {
		input.QuotaMinutes = defaults.QuotaMinutes
	}
	if input.QuotaMinutes < 1 || input.QuotaMinutes > input.WindowHours*60 {
		return ManagedConfig{}, errors.New("quota_minutes must be within the configured window duration")
	}
	if input.WorkStart == "" {
		input.WorkStart = defaults.WorkStart
	}
	if input.LunchStart == "" {
		input.LunchStart = defaults.LunchStart
	}
	if input.LunchEnd == "" {
		input.LunchEnd = defaults.LunchEnd
	}
	if input.WorkEnd == "" {
		input.WorkEnd = defaults.WorkEnd
	}
	if input.AnchorTime == "" {
		input.AnchorTime = defaults.AnchorTime
	}
	workStart, err := managedClockMinutes(input.WorkStart)
	if err != nil {
		return ManagedConfig{}, fmt.Errorf("invalid work_start: %w", err)
	}
	lunchStart, err := managedClockMinutes(input.LunchStart)
	if err != nil {
		return ManagedConfig{}, fmt.Errorf("invalid lunch_start: %w", err)
	}
	lunchEnd, err := managedClockMinutes(input.LunchEnd)
	if err != nil {
		return ManagedConfig{}, fmt.Errorf("invalid lunch_end: %w", err)
	}
	workEnd, err := managedClockMinutes(input.WorkEnd)
	if err != nil {
		return ManagedConfig{}, fmt.Errorf("invalid work_end: %w", err)
	}
	if _, err := managedClockMinutes(input.AnchorTime); err != nil {
		return ManagedConfig{}, fmt.Errorf("invalid anchor_time: %w", err)
	}
	if !(workStart < lunchStart && lunchStart < lunchEnd && lunchEnd < workEnd) {
		return ManagedConfig{}, errors.New("work times must satisfy work_start < lunch_start < lunch_end < work_end")
	}
	if input.HealthThreshold == 0 {
		input.HealthThreshold = defaults.HealthThreshold
	}
	if input.HealthThreshold < 1 || input.HealthThreshold > 100 {
		return ManagedConfig{}, errors.New("health_threshold must be between 1 and 100")
	}
	return input, nil
}

func normalizeManagedSelectionList(value string) string {
	seen := make(map[string]struct{})
	var values []string
	for _, raw := range strings.Split(value, ",") {
		item := strings.ToLower(strings.TrimSpace(raw))
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		values = append(values, item)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func normalizeManagedWeekdays(value string) (string, error) {
	seen := make(map[int]struct{})
	var days []int
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		day, err := strconv.Atoi(raw)
		if err != nil || day < 1 || day > 7 {
			return "", errors.New("weekdays must contain values from 1 through 7")
		}
		if _, exists := seen[day]; exists {
			continue
		}
		seen[day] = struct{}{}
		days = append(days, day)
	}
	if len(days) == 0 {
		return "", errors.New("at least one weekday must be selected")
	}
	sort.Ints(days)
	parts := make([]string, len(days))
	for index, day := range days {
		parts[index] = strconv.Itoa(day)
	}
	return strings.Join(parts, ","), nil
}

func managedClockMinutes(value string) (int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return 0, errors.New("use HH:mm")
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func managedWeekdayNumber(value time.Weekday) int {
	if value == time.Sunday {
		return 7
	}
	return int(value)
}

func managedWeekdaySet(config ManagedConfig) map[int]struct{} {
	set := make(map[int]struct{})
	for _, raw := range strings.Split(config.Weekdays, ",") {
		if day, err := strconv.Atoi(raw); err == nil {
			set[day] = struct{}{}
		}
	}
	return set
}

func managedDayEnabled(config ManagedConfig, value time.Time) bool {
	_, ok := managedWeekdaySet(config)[managedWeekdayNumber(value.Weekday())]
	return ok
}

func managedTimeOnDay(day time.Time, hhmm string, location *time.Location) time.Time {
	clock, _ := time.Parse("15:04", hhmm)
	return time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
}

func managedNextEnabledDayAt(config ManagedConfig, now time.Time, hhmm string, allowToday bool) (time.Time, error) {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	localNow := now.In(location)
	for offset := 0; offset <= 14; offset++ {
		if offset == 0 && !allowToday {
			continue
		}
		day := localNow.AddDate(0, 0, offset)
		if !managedDayEnabled(config, day) {
			continue
		}
		candidate := managedTimeOnDay(day, hhmm, location)
		if candidate.After(localNow) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unable to find the next enabled day")
}

func managedNextLegacyRun(config ManagedConfig, now time.Time) (time.Time, error) {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	localNow := now.In(location)
	if config.Mode == "interval" {
		candidate := now.Add(time.Duration(config.IntervalMin)*time.Minute + intervalJitter())
		if managedDayEnabled(config, candidate.In(location)) {
			return candidate.UTC(), nil
		}
		return managedNextEnabledDayAt(config, localNow, config.WorkStart, true)
	}
	times, err := parseDailyTimes(config.DailyTimes)
	if err != nil {
		return time.Time{}, err
	}
	for dayOffset := 0; dayOffset <= 14; dayOffset++ {
		day := localNow.AddDate(0, 0, dayOffset)
		if !managedDayEnabled(config, day) {
			continue
		}
		for _, value := range times {
			candidate := managedTimeOnDay(day, value, location)
			if candidate.After(localNow) {
				return candidate.UTC(), nil
			}
		}
	}
	return time.Time{}, errors.New("unable to calculate next scheduled run")
}

func managedSelectionKey(file AuthFile) string {
	if email := strings.ToLower(strings.TrimSpace(file.Email)); email != "" {
		return email
	}
	return "@auth:" + strings.ToLower(strings.TrimSpace(file.AuthIndex))
}

func managedExcludedSet(config ManagedConfig) map[string]struct{} {
	set := make(map[string]struct{})
	for _, raw := range strings.Split(config.ExcludedEmails, ",") {
		if item := strings.ToLower(strings.TrimSpace(raw)); item != "" {
			set[item] = struct{}{}
		}
	}
	return set
}

func (r *Runtime) managedCodexAccounts(ctx context.Context, config ManagedConfig) ([]AuthFile, error) {
	files, err := r.host.ListAuthFiles(ctx)
	if err != nil {
		return nil, err
	}
	excluded := managedExcludedSet(config)
	var result []AuthFile
	for _, file := range files {
		if !isCodexAuth(file) {
			continue
		}
		if _, skip := excluded[managedSelectionKey(file)]; skip {
			continue
		}
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Email == result[j].Email {
			return result[i].AuthIndex < result[j].AuthIndex
		}
		return result[i].Email < result[j].Email
	})
	return result, nil
}

func (r *Runtime) ManagedAccounts() ([]map[string]any, error) {
	views, err := r.Accounts()
	if err != nil {
		return nil, err
	}

	// Window optimization executes accounts independently. Runtime.Accounts()
	// only projects state.Latest, which may contain a single account, so merge
	// the newest persisted result for every auth index to keep the account table
	// stable between staggered window executions.
	latestByIndex := make(map[string]AccountResult)
	for _, record := range r.History() { // History returns newest runs first.
		for _, account := range record.Accounts {
			if _, exists := latestByIndex[account.AuthIndex]; !exists {
				latestByIndex[account.AuthIndex] = account
			}
		}
	}
	for index := range views {
		if views[index].Disabled {
			continue
		}
		if account, exists := latestByIndex[views[index].AuthIndex]; exists {
			views[index].AccountID = account.AccountID
			views[index].Status = account.Status
			views[index].Healthy = account.Healthy
			views[index].HTTPStatus = account.HTTPStatus
			views[index].LatencyMS = account.LatencyMS
			views[index].CheckedAt = account.CheckedAt
			views[index].ErrorCode = account.ErrorCode
			views[index].ErrorMessage = account.ErrorMessage
		}
	}

	managed := managedStateFor(r)
	managed.mu.RLock()
	config := managed.state.Config
	managed.mu.RUnlock()
	excluded := managedExcludedSet(config)
	result := make([]map[string]any, 0, len(views))
	for _, view := range views {
		raw, _ := json.Marshal(view)
		var item map[string]any
		_ = json.Unmarshal(raw, &item)
		key := strings.ToLower(strings.TrimSpace(view.Email))
		if key == "" {
			key = "@auth:" + strings.ToLower(strings.TrimSpace(view.AuthIndex))
		}
		_, skip := excluded[key]
		item["selection_key"] = key
		item["selected"] = !skip
		result = append(result, item)
	}
	return result, nil
}
