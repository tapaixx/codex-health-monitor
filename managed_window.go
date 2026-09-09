package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (r *Runtime) managedWindowLoopStep(ctx context.Context, config ManagedConfig) bool {
	accounts, err := r.managedCodexAccounts(ctx, config)
	if err != nil {
		r.setManagedNext(time.Time{}, err)
		return false
	}
	managed := managedStateFor(r)
	now := time.Now().UTC()
	managed.mu.Lock()
	selected := make(map[string]AuthFile, len(accounts))
	for _, account := range accounts {
		selected[account.AuthIndex] = account
		state := managed.state.Accounts[account.AuthIndex]
		if state.NextRunAt.IsZero() {
			state.NextRunAt, _ = managedFirstWindowRun(config, now)
			managed.state.Accounts[account.AuthIndex] = state
		}
	}
	for key := range managed.state.Accounts {
		if _, exists := selected[key]; !exists {
			delete(managed.state.Accounts, key)
		}
	}
	states := cloneManagedAccounts(managed.state.Accounts)
	managed.mu.Unlock()
	managed.persist(r)

	var next time.Time
	for _, state := range states {
		if state.NextRunAt.IsZero() {
			continue
		}
		if next.IsZero() || state.NextRunAt.Before(next) {
			next = state.NextRunAt
		}
	}
	if next.IsZero() {
		// No selected accounts. Keep the worker responsive to future account
		// discovery or selection changes without creating requests.
		next = time.Now().Add(time.Minute)
	}
	r.setManagedNext(next, nil)
	if !managedWaitUntil(ctx, next) {
		return false
	}

	now = time.Now().UTC()
	for _, account := range accounts {
		managed.mu.RLock()
		state, exists := managed.state.Accounts[account.AuthIndex]
		managed.mu.RUnlock()
		if !exists || state.NextRunAt.IsZero() || state.NextRunAt.After(now.Add(time.Second)) {
			continue
		}
		if !r.executeManagedWindowAccount(ctx, config, account, state) {
			return false
		}
	}
	return true
}

func managedFirstWindowRun(config ManagedConfig, now time.Time) (time.Time, error) {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	localNow := now.In(location)
	for offset := 0; offset <= 14; offset++ {
		day := localNow.AddDate(0, 0, offset)
		if !managedDayEnabled(config, day) {
			continue
		}
		anchor := managedTimeOnDay(day, config.AnchorTime, location)
		end := managedTimeOnDay(day, config.WorkEnd, location)
		if offset == 0 {
			if localNow.Before(anchor) {
				return anchor.Add(managedWindowJitter()).UTC(), nil
			}
			if localNow.Before(end) {
				return localNow.Add(managedWindowJitter()).UTC(), nil
			}
			continue
		}
		return anchor.Add(managedWindowJitter()).UTC(), nil
	}
	return time.Time{}, errors.New("unable to calculate the first window run")
}

func managedNormalizeWindowCandidate(config ManagedConfig, candidate time.Time) time.Time {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return time.Time{}
	}
	local := candidate.In(location)
	if managedDayEnabled(config, local) {
		anchor := managedTimeOnDay(local, config.AnchorTime, location)
		end := managedTimeOnDay(local, config.WorkEnd, location)
		if local.Before(anchor) {
			return anchor.Add(managedWindowJitter()).UTC()
		}
		if !local.After(end) {
			return candidate.UTC()
		}
	}
	next, _ := managedNextEnabledDayAt(config, local, config.AnchorTime, false)
	if next.IsZero() {
		return time.Time{}
	}
	return next.Add(managedWindowJitter()).UTC()
}

func shouldRetryManagedWindow(result AccountResult) (bool, string) {
	switch result.ErrorCode {
	case "rate_limited", "payment_required":
		return true, "quota_retry"
	case "network_error", "timeout", "upstream_error":
		return true, "window_retry"
	default:
		return false, ""
	}
}

func (r *Runtime) executeManagedWindowAccount(ctx context.Context, config ManagedConfig, account AuthFile, state ManagedAccountState) bool {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		managed := managedStateFor(r)
		managed.mu.Lock()
		state.NextRunAt = time.Now().UTC().Add(time.Minute)
		managed.state.Accounts[account.AuthIndex] = state
		managed.mu.Unlock()
		managed.persist(r)
		return true
	}
	r.running = true
	r.state.Running = true
	r.state.UpdatedAt = time.Now().UTC()
	r.mu.Unlock()
	r.persistState()

	planned := state.NextRunAt
	trigger := "window_optimized"
	if state.RetryPending {
		trigger = state.RetryTrigger
		if trigger == "" {
			trigger = "window_retry"
		}
	}
	started := time.Now().UTC()
	result := r.probeAccount(ctx, account, config.TimeoutSec)
	finished := time.Now().UTC()
	record := RunRecord{ID: fmt.Sprintf("%d", started.UnixNano()), Trigger: trigger, StartedAt: started, FinishedAt: finished, Total: 1, Accounts: []AccountResult{result}}
	if result.Healthy {
		record.Healthy = 1
	} else {
		record.Unhealthy = 1
	}

	step := time.Duration(config.WindowHours)*time.Hour + managedWindowJitter()
	nextState := state
	if result.Healthy {
		nextState.LastSuccessAt = finished
		nextState.LastPlannedAt = planned
		nextState.RetryPending = false
		nextState.RetryTrigger = ""
		nextState.FallbackAt = time.Time{}
		nextState.NextRunAt = managedNormalizeWindowCandidate(config, finished.Add(step))
	} else if state.RetryPending {
		// The compensation policy is deliberately one-shot.
		nextState.RetryPending = false
		nextState.RetryTrigger = ""
		nextState.NextRunAt = managedNormalizeWindowCandidate(config, state.FallbackAt)
		nextState.FallbackAt = time.Time{}
	} else if retry, retryTrigger := shouldRetryManagedWindow(result); retry {
		nextState.FallbackAt = managedNormalizeWindowCandidate(config, planned.Add(step))
		nextState.RetryPending = true
		nextState.RetryTrigger = retryTrigger
		nextState.NextRunAt = finished.Add(managedRetryDelay)
	} else {
		nextState.NextRunAt = managedNormalizeWindowCandidate(config, planned.Add(step))
	}

	r.mu.Lock()
	r.running = false
	r.state.Running = false
	r.state.Latest = &record
	r.state.UpdatedAt = finished
	r.history = append(r.history, record)
	if len(r.history) > maxHistory {
		r.history = append([]RunRecord(nil), r.history[len(r.history)-maxHistory:]...)
	}
	r.mu.Unlock()

	managed := managedStateFor(r)
	managed.mu.Lock()
	managed.state.Accounts[account.AuthIndex] = nextState
	managed.state.Planned[record.ID] = planned
	managed.trimPlannedLocked(r)
	managed.mu.Unlock()
	managed.persist(r)
	r.persistAll()
	return ctx.Err() == nil
}
