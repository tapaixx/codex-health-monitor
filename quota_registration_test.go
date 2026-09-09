package main

import "testing"

func TestRuntimeManagementRegistrationIncludesQuotaRoutes(t *testing.T) {
	registration := managementRegistrationPayloadForID(pluginName)
	if len(registration.Routes) != 10 {
		t.Fatalf("runtime registration has %d routes, want 10", len(registration.Routes))
	}
	want := map[string]string{
		"/plugins/codex-health-monitor/quota":             "GET",
		"/plugins/codex-health-monitor/quota/refresh":     "POST",
		"/plugins/codex-health-monitor/quota/refresh-all": "POST",
		"/plugins/codex-health-monitor/quota/reset":       "POST",
	}
	for _, route := range registration.Routes {
		if method, ok := want[route.Path]; ok {
			if route.Method != method {
				t.Fatalf("route %s method = %s, want %s", route.Path, route.Method, method)
			}
			delete(want, route.Path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing quota routes: %v", want)
	}
}
