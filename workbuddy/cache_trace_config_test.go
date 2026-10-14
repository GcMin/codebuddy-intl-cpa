package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// cacheTraceTestEnv points the state + usage dir at a temp dir and clears the
// module-level caches so every case starts from an empty state file. Returns
// the dir for direct file assertions.
func cacheTraceTestEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WORKBUDDY_DATA_DIR", dir)
	t.Setenv("WB_CACHE_TRACE", "")
	reset := func() {
		modelsStateMu.Lock()
		modelsStateCache = nil
		modelsStatePath = ""
		modelsStateMu.Unlock()
		cacheTraceEnabled.Store(false)
	}
	reset()
	t.Cleanup(reset)
	return dir
}

func postCacheTrace(t *testing.T, body string) map[string]any {
	t.Helper()
	return handleCacheTraceConfig(pluginapi.ManagementRequest{Body: []byte(body)})
}

func boolPtr(v bool) *bool { return &v }

func stateCacheTrace(t *testing.T, dir string) (bool, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, mmStateFileName))
	if err != nil {
		return false, false
	}
	var st struct {
		CacheTrace *bool `json:"cache_trace"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}
	if st.CacheTrace == nil {
		return false, false
	}
	return *st.CacheTrace, true
}

func TestCacheTraceConfigPersistsAndOverridesEnv(t *testing.T) {
	dir := cacheTraceTestEnv(t)

	// No env, no override: both the default and the status agree it is off.
	applyCacheTracePreference()
	status := handleCacheTraceStatus()
	if status["enabled"] != false || status["has_override"] != false || status["env_default"] != false {
		t.Fatalf("unexpected default status: %+v", status)
	}

	// The panel flip applies to the hot path immediately and writes the state.
	out := postCacheTrace(t, `{"enabled":true}`)
	if out["enabled"] != true || out["persistent"] != true {
		t.Fatalf("enable response: %+v", out)
	}
	if !cacheTraceEnabled.Load() {
		t.Fatal("flip did not reach the runtime switch")
	}
	if on, ok := stateCacheTrace(t, dir); !ok || !on {
		t.Fatalf("state file missing cache_trace=true (on=%v ok=%v)", on, ok)
	}
	if status = handleCacheTraceStatus(); status["has_override"] != true {
		t.Fatalf("status did not report the persisted override: %+v", status)
	}

	// Simulated restart: the persisted choice must win over the (off) env.
	modelsStateMu.Lock()
	modelsStateCache, modelsStatePath = nil, ""
	modelsStateMu.Unlock()
	cacheTraceEnabled.Store(false)
	applyCacheTracePreference()
	if !cacheTraceEnabled.Load() {
		t.Fatal("persisted override did not survive a restart")
	}

	// And a later env=1 deployment must not silently re-enable what the panel
	// turned off (the explicit choice is the stronger signal).
	postCacheTrace(t, `{"enabled":false}`)
	if off, ok := stateCacheTrace(t, dir); !ok || off {
		t.Fatalf("state file missing cache_trace=false (off=%v ok=%v)", off, ok)
	}
	t.Setenv("WB_CACHE_TRACE", "1")
	modelsStateMu.Lock()
	modelsStateCache, modelsStatePath = nil, ""
	modelsStateMu.Unlock()
	applyCacheTracePreference()
	if cacheTraceEnabled.Load() {
		t.Fatal("env default overrode an explicit panel choice")
	}
}

func TestCacheTraceEnvDefaultAppliesWithoutOverride(t *testing.T) {
	cacheTraceTestEnv(t)
	t.Setenv("WB_CACHE_TRACE", "yes")
	applyCacheTracePreference()
	if !cacheTraceEnabled.Load() {
		t.Fatal("WB_CACHE_TRACE=yes was not honoured as the default")
	}
	status := handleCacheTraceStatus()
	if status["enabled"] != true || status["env_default"] != true || status["has_override"] != false {
		t.Fatalf("unexpected env status: %+v", status)
	}
}

func TestCacheTraceConfigRejectsBadBody(t *testing.T) {
	cacheTraceTestEnv(t)
	out := postCacheTrace(t, `{}`)
	if out["error"] == nil {
		t.Fatalf("missing enabled field must be rejected: %+v", out)
	}
	if cacheTraceEnabled.Load() {
		t.Fatal("rejected body must not flip the switch")
	}
}
