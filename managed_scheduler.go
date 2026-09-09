package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

func (r *Runtime) ActivateManagedScheduler() error {
	managed := managedStateFor(r)
	if managed.loadErr != nil {
		r.host.Log(context.Background(), "warn", "window optimizer state could not be loaded; defaults will be used", map[string]any{"error_code": "window_state_load_failed"})
	}
	managed.mu.Lock()
	normalized, err := normalizeManagedConfig(managed.state.Config)
	if err != nil {
		normalized = defaultManagedConfig()
	}
	managed.state.Version = managedStateVersion
	managed.state.Config = normalized
	if managed.state.Accounts == nil {
		managed.state.Accounts = make(map[string]ManagedAccountState)
	}
	if managed.state.Planned == nil {
		managed.state.Planned = make(map[string]time.Time)
	}
	managed.mu.Unlock()

	// The managed account-selection model must always see all Codex accounts.
	// Clear the legacy positive target list before replacing the legacy worker.
	r.mu.Lock()
	r.state.Schedule.TargetEmails = ""
	r.mu.Unlock()
	r.persistState()
	managed.persist(r)
	return r.restartManagedScheduler()
}

func (r *Runtime) UpdateManagedSchedule(input ManagedConfig) error {
	normalized, err := normalizeManagedConfig(input)
	if err != nil {
		return err
	}
	managed := managedStateFor(r)
	managed.mu.Lock()
	old := managed.state.Config
	managed.state.Version = managedStateVersion
	managed.state.Config = normalized
	if managed.state.Accounts == nil || old != normalized {
		managed.state.Accounts = make(map[string]ManagedAccountState)
	}
	if managed.state.Planned == nil {
		managed.state.Planned = make(map[string]time.Time)
	}
	managed.mu.Unlock()
	managed.persist(r)

	r.mu.Lock()
	r.state.Schedule.TargetEmails = ""
	r.state.ScheduleError = ""
	r.state.UpdatedAt = time.Now().UTC()
	r.mu.Unlock()
	r.persistState()
	return r.restartManagedScheduler()
}

func (r *Runtime) ManagedScheduleStatus() map[string]any {
	managed := managedStateFor(r)
	managed.mu.RLock()
	config := managed.state.Config
	accounts := cloneManagedAccounts(managed.state.Accounts)
	managed.mu.RUnlock()
	r.mu.RLock()
	next := r.state.NextRunAt
	errText := r.state.ScheduleError
	r.mu.RUnlock()
	return map[string]any{
		"schedule":        config,
		"next_run_at":     next,
		"schedule_error":  errText,
		"window_accounts": accounts,
	}
}

func (r *Runtime) restartManagedScheduler() error {
	managed := managedStateFor(r)
	managed.mu.RLock()
	config := managed.state.Config
	managed.mu.RUnlock()

	r.schedMu.Lock()
	defer r.schedMu.Unlock()
	r.mu.Lock()
	oldCancel := r.workerCancel
	oldDone := r.workerDone
	r.workerCancel = nil
	r.workerDone = nil
	r.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
		if oldDone != nil {
			<-oldDone
		}
	}
	if !config.Enabled {
		r.mu.Lock()
		r.state.NextRunAt = time.Time{}
		r.mu.Unlock()
		r.persistState()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.mu.Lock()
	r.workerCancel = cancel
	r.workerDone = done
	r.mu.Unlock()
	go r.managedSchedulerLoop(ctx, done)
	return nil
}

func (r *Runtime) managedSchedulerLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		managed := managedStateFor(r)
		managed.mu.RLock()
		config := managed.state.Config
		managed.mu.RUnlock()
		if !config.Enabled {
			return
		}
		if config.Mode == "window_optimized" {
			if !r.managedWindowLoopStep(ctx, config) {
				return
			}
			continue
		}
		next, err := managedNextLegacyRun(config, time.Now())
		r.setManagedNext(next, err)
		if err != nil {
			return
		}
		if !managedWaitUntil(ctx, next) {
			return
		}
		doneRun, runErr := r.startManagedRun(config.Mode, next, config)
		if runErr != nil {
			if errors.Is(runErr, ErrRunInProgress) {
				if !waitForDelay(ctx, time.Minute) {
					return
				}
				continue
			}
			r.host.Log(context.Background(), "error", "managed scheduled Codex check could not start", map[string]any{"error_code": "start_failed"})
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-doneRun:
		}
	}
}

func (r *Runtime) setManagedNext(next time.Time, err error) {
	r.mu.Lock()
	if err != nil {
		r.state.ScheduleError = "unable to calculate next run"
		r.state.NextRunAt = time.Time{}
	} else {
		r.state.ScheduleError = ""
		r.state.NextRunAt = next.UTC()
	}
	r.state.UpdatedAt = time.Now().UTC()
	r.mu.Unlock()
	r.persistState()
}

func managedWaitUntil(ctx context.Context, target time.Time) bool {
	delay := time.Until(target)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *Runtime) StartManagedManualRun() (<-chan struct{}, error) {
	managed := managedStateFor(r)
	managed.mu.RLock()
	config := managed.state.Config
	managed.mu.RUnlock()
	return r.startManagedRun("manual", time.Time{}, config)
}

func (r *Runtime) startManagedRun(trigger string, planned time.Time, config ManagedConfig) (<-chan struct{}, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil, ErrRunInProgress
	}
	r.running = true
	r.state.Running = true
	r.state.UpdatedAt = time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	r.runCancel = cancel
	done := make(chan struct{})
	r.mu.Unlock()
	r.persistState()
	go func() {
		defer close(done)
		record := r.executeManagedBatch(ctx, trigger, planned, config)
		r.finishManagedRecord(record, planned)
	}()
	return done, nil
}

func (r *Runtime) executeManagedBatch(ctx context.Context, trigger string, planned time.Time, config ManagedConfig) RunRecord {
	started := time.Now().UTC()
	record := RunRecord{ID: fmt.Sprintf("%d", started.UnixNano()), Trigger: trigger, StartedAt: started}
	files, err := r.managedCodexAccounts(ctx, config)
	if err != nil {
		record.ErrorCode = "account_discovery_failed"
		record.ErrorMessage = "CPA could not list Codex credentials."
		record.FinishedAt = time.Now().UTC()
		return record
	}
	results := make([]AccountResult, len(files))
	var wg sync.WaitGroup
	for index, file := range files {
		wg.Add(1)
		go func(i int, account AuthFile) {
			defer wg.Done()
			if (trigger == "interval" || trigger == "daily_times") && !waitForDelay(ctx, accountJitter()) {
				results[i] = r.probeAccount(ctx, account, config.TimeoutSec)
				return
			}
			results[i] = r.probeAccount(ctx, account, config.TimeoutSec)
		}(index, file)
	}
	wg.Wait()
	record.Accounts = results
	record.Total = len(results)
	for _, result := range results {
		if result.Healthy {
			record.Healthy++
		} else {
			record.Unhealthy++
		}
	}
	record.FinishedAt = time.Now().UTC()
	return record
}

func (r *Runtime) finishManagedRecord(record RunRecord, planned time.Time) {
	r.mu.Lock()
	r.running = false
	r.runCancel = nil
	r.state.Running = false
	r.state.Latest = &record
	r.state.UpdatedAt = time.Now().UTC()
	r.history = append(r.history, record)
	if len(r.history) > maxHistory {
		r.history = append([]RunRecord(nil), r.history[len(r.history)-maxHistory:]...)
	}
	r.mu.Unlock()
	managed := managedStateFor(r)
	managed.mu.Lock()
	if !planned.IsZero() {
		managed.state.Planned[record.ID] = planned.UTC()
	}
	managed.trimPlannedLocked(r)
	managed.mu.Unlock()
	managed.persist(r)
	r.persistAll()
}

func (m *managedRuntimeState) trimPlannedLocked(r *Runtime) {
	valid := make(map[string]struct{})
	r.mu.RLock()
	for _, record := range r.history {
		valid[record.ID] = struct{}{}
	}
	r.mu.RUnlock()
	for id := range m.state.Planned {
		if _, exists := valid[id]; !exists {
			delete(m.state.Planned, id)
		}
	}
}

func (r *Runtime) ManagedHistory() []map[string]any {
	records := r.History()
	managed := managedStateFor(r)
	managed.mu.RLock()
	planned := cloneManagedPlanned(managed.state.Planned)
	managed.mu.RUnlock()
	result := make([]map[string]any, 0, len(records))
	for _, record := range records {
		raw, _ := json.Marshal(record)
		var item map[string]any
		_ = json.Unmarshal(raw, &item)
		if value, ok := planned[record.ID]; ok && !value.IsZero() {
			item["planned_at"] = value
		}
		result = append(result, item)
	}
	return result
}
