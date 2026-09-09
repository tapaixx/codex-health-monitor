package main

import (
	"testing"
	"time"
)

func TestManagedDefaults(t *testing.T) {
	cfg, err := normalizeManagedConfig(defaultManagedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "window_optimized" || cfg.WindowHours != 5 || cfg.QuotaMinutes != 60 || cfg.AnchorTime != "06:59" {
		t.Fatalf("unexpected managed defaults: %+v", cfg)
	}
	if cfg.Weekdays != "1,2,3,4,5" || cfg.WorkStart != "09:00" || cfg.LunchStart != "12:00" || cfg.LunchEnd != "13:30" || cfg.WorkEnd != "19:00" {
		t.Fatalf("unexpected managed workday: %+v", cfg)
	}
}

func TestManagedNextWindowSkipsWeekend(t *testing.T) {
	cfg, err := normalizeManagedConfig(defaultManagedConfig())
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation(cfg.Timezone)
	previous := managedWindowJitter
	managedWindowJitter = func() time.Duration { return 0 }
	t.Cleanup(func() { managedWindowJitter = previous })

	// Sunday evening must schedule the next enabled day (Monday) at the anchor.
	now := time.Date(2026, 9, 13, 20, 0, 0, 0, loc)
	got, err := managedFirstWindowRun(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 14, 6, 59, 0, 0, loc).UTC()
	if !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

func TestManagedRetryPolicyIsSingleCompensationClass(t *testing.T) {
	for _, test := range []struct {
		code    string
		retry   bool
		trigger string
	}{
		{"rate_limited", true, "quota_retry"},
		{"payment_required", true, "quota_retry"},
		{"network_error", true, "window_retry"},
		{"timeout", true, "window_retry"},
		{"upstream_error", true, "window_retry"},
		{"unauthorized", false, ""},
		{"forbidden", false, ""},
	} {
		retry, trigger := shouldRetryManagedWindow(AccountResult{ErrorCode: test.code})
		if retry != test.retry || trigger != test.trigger {
			t.Fatalf("%s: retry=%v trigger=%q, want %v %q", test.code, retry, trigger, test.retry, test.trigger)
		}
	}
}

func TestManagedConfigValidatesWorkdayAndWeekdays(t *testing.T) {
	cfg := defaultManagedConfig()
	cfg.Weekdays = "7,1,1,5"
	normalized, err := normalizeManagedConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Weekdays != "1,5,7" {
		t.Fatalf("weekdays = %q", normalized.Weekdays)
	}
	cfg.LunchStart = "08:00"
	if _, err := normalizeManagedConfig(cfg); err == nil {
		t.Fatal("expected invalid workday ordering to fail")
	}
}
