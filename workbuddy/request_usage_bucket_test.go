package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// stubUsageAuth swaps the host auth snapshot hooks used by the usage metadata
// resolver, keeping tests off the cgo bridge. Returns a restore func.
func stubUsageAuth(t *testing.T, list func() ([]pluginapi.HostAuthFileEntry, error), get func(string) (*storedAuth, error)) func() {
	t.Helper()
	oldList, oldGet := usageAuthListFn, usageAuthGetFn
	usageAuthListFn, usageAuthGetFn = list, get
	return func() {
		usageAuthListFn, usageAuthGetFn = oldList, oldGet
	}
}

func resetUsageMetaCache(t *testing.T) {
	t.Helper()
	old := usageMetaCache
	usageMetaCache = &usageMetaResolver{}
	t.Cleanup(func() { usageMetaCache = old })
}

func testAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	return []pluginapi.HostAuthFileEntry{
		{ID: "auth-alice-id", AuthIndex: "idx-alice", Name: "workbuddy-uid-alice.json"},
	}, nil
}

func testAuthGet(index string) (*storedAuth, error) {
	if index != "idx-alice" {
		return nil, errors.New("not found")
	}
	return &storedAuth{
		Auth:    storedTokens{Domain: "global"},
		Account: storedAccount{UID: "uid-alice", Nickname: "Alice"},
	}, nil
}

func TestUsageBucketKey_RekeysKnownCredential(t *testing.T) {
	resetUsageMetaCache(t)
	restore := stubUsageAuth(t, testAuthList, testAuthGet)
	defer restore()

	cases := []struct {
		name string
		rec  requestUsageRecord
		want string
	}{
		{
			name: "raw auth id resolves to current index",
			rec:  requestUsageRecord{AuthID: "auth-alice-id"},
			want: "idx-alice",
		},
		{
			name: "account uid resolves to current index",
			rec:  requestUsageRecord{AuthUID: "uid-alice"},
			want: "idx-alice",
		},
		{
			name: "valid stored index is kept (no-op remap)",
			rec:  requestUsageRecord{AuthIndex: "idx-alice", AuthID: "auth-alice-id"},
			want: "idx-alice",
		},
		{
			name: "unknown ids keep their original key",
			rec:  requestUsageRecord{AuthIndex: "orphan-key", AuthID: "unknown-id"},
			want: "orphan-key",
		},
		{
			name: "empty record falls through to empty (bucket _unknown)",
			rec:  requestUsageRecord{},
			want: "",
		},
	}
	for _, tc := range cases {
		if got := usageBucketKey(tc.rec); got != tc.want {
			t.Fatalf("%s: bucket key = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRequestUsageStore_RekeyResolvedMergesPhantom(t *testing.T) {
	resetUsageMetaCache(t)
	restore := stubUsageAuth(t, testAuthList, testAuthGet)
	defer restore()

	s := &requestUsageStore{perDay: map[string]map[string]*dayAgg{
		"2026-10-19": {
			// phantom bucket: raw auth ID written during a snapshot miss
			"auth-alice-id": {CreditMilli: 12_345, Requests: 7},
			// real bucket that must absorb the phantom's totals
			"idx-alice": {CreditMilli: 1_000, Requests: 1},
			// unrelated orphan that resolves to nothing and must stay
			"gone-account": {CreditMilli: 5_000, Requests: 2},
		},
	}}

	s.rekeyResolved()
	m := s.perDay["2026-10-19"]
	if len(m) != 2 {
		t.Fatalf("bucket count = %d, want 2 (phantom merged, orphan kept): %+v", len(m), m)
	}
	real := m["idx-alice"]
	if real == nil {
		t.Fatal("idx-alice bucket missing")
	}
	if real.CreditMilli != 13_345 || real.Requests != 8 {
		t.Fatalf("merged totals = %+v, want credit 13345/req 8", real)
	}
	if m["gone-account"] == nil {
		t.Fatal("unresolvable bucket must be kept, not dropped")
	}
}

func TestUsageMetaResolver_EmptySnapshotRetriesFaster(t *testing.T) {
	resetUsageMetaCache(t)

	failing := true
	restore := stubUsageAuth(t, func() ([]pluginapi.HostAuthFileEntry, error) {
		if failing {
			return nil, errors.New("host not ready")
		}
		return testAuthList()
	}, testAuthGet)
	defer restore()

	// First resolve during the outage: empty metadata, error remembered.
	m := usageMetaCache.resolve("auth-alice-id", "")
	if m.Nickname != "" || m.AuthIndex != "" {
		t.Fatalf("outage resolve should be empty, got %+v", m)
	}
	if got := usageMetaCache.retryAfter(); got != usageMetaRetryAfter {
		t.Fatalf("retryAfter after failure = %v, want %v", got, usageMetaRetryAfter)
	}

	// Host recovers; expiry the short window and resolve again.
	failing = false
	usageMetaCache.refreshed = time.Now().Add(-2 * usageMetaRetryAfter)
	m = usageMetaCache.resolve("auth-alice-id", "")
	if m.Nickname != "Alice" || m.AuthIndex != "idx-alice" {
		t.Fatalf("recovery resolve = %+v, want Alice/idx-alice", m)
	}
	if got := usageMetaCache.retryAfter(); got != usageMetaTTL {
		t.Fatalf("retryAfter after recovery = %v, want %v", got, usageMetaTTL)
	}
}

func TestBuildUsageRecord_CarriesAuthUID(t *testing.T) {
	retrieved := buildUsageRecord("cb/glm-5.3", "glm-5.3", "auth-1", "uid-1", time.Now().Add(-time.Second), usage.Detail{}, 0, false)
	if retrieved.AuthUID != "uid-1" {
		t.Fatalf("AuthUID = %q, want uid-1", retrieved.AuthUID)
	}
}

func TestRequestUsageRecord_AuthUIDJSONCompat(t *testing.T) {
	// New records marshal the UID field.
	rec := requestUsageRecord{AuthID: "a", AuthUID: "u"}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back requestUsageRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.AuthUID != "u" || back.AuthID != "a" {
		t.Fatalf("round-trip = %+v", back)
	}
	// Historical JSONL lines without auth_uid still decode.
	var legacy requestUsageRecord
	if err := json.Unmarshal([]byte(`{"timestamp":"2026-10-13T04:36:22Z","auth_index":"05d5","auth_id":"05d5"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.AuthUID != "" {
		t.Fatalf("legacy auth_uid should be empty, got %q", legacy.AuthUID)
	}
}
