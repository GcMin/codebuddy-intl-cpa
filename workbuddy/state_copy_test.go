package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// loadState() returns a *copy* of the cached state file, and it builds that copy
// field by field. Forgetting a field is silent: the data is on disk, every
// reader sees the zero value, and the feature looks broken. That happened three
// times (DailyReport, TrialClaimed, and the model settings), so this test
// enumerates the struct with reflection and fails when a field is not copied.
//
// It is deliberately behavioural rather than a check on the literal source: it
// writes a fully populated state file, reloads it through loadState(), and
// compares every field. A new field that loadState forgets shows up as a diff.
func TestLoadStateCopiesEveryField(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKBUDDY_DATA_DIR", dir)

	// Drop any cached state so the file we write is the one that gets read.
	modelsStateMu.Lock()
	modelsStateCache = nil
	modelsStatePath = ""
	modelsStateMu.Unlock()
	t.Cleanup(func() {
		modelsStateMu.Lock()
		modelsStateCache = nil
		modelsStatePath = ""
		modelsStateMu.Unlock()
	})

	when := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	// Disabled/Custom are non-nil empty for the realm that has none: cloneSettings
	// normalises nil slices to empty ones, so a nil fixture would fail on the
	// normalisation rather than on a dropped field.
	want := wbStateFile{
		Realm: map[string]mmSettings{
			realmGlobal: {
				Disabled: []string{"gpt-6-sol"},
				Custom:   []mmCustomModel{{ID: "custom-x", Name: "Custom X", Ctx: "128k"}},
				Prefix:   mmPrefixState{On: true, Val: "cb/"},
				Fallback: mmFallbackState{On: true, Model: "gpt-6.1-sol"},
			},
			realmCN: {
				Disabled: []string{},
				Custom:   []mmCustomModel{},
				Prefix:   mmPrefixState{On: true, Val: "cn/"},
			},
		},
		Update: mmUpdateState{SeenVersion: "0.9.9"},
		DailyReport: &persistedDailyReport{
			When: when,
			Rows: map[string]persistedDailyRow{
				"auth-one": {Day: "2026-10-14", Score: 2, Lit: true, Status: "already-lit", Detail: "ok"},
			},
		},
		TrialClaimed: map[string]bool{"auth-two": true},
		CacheTrace:   boolPtr(true),
	}

	raw, err := json.MarshalIndent(&want, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(wbStatePath(), append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}

	// Field-by-field so a failure names the field that was dropped instead of
	// dumping the whole struct.
	rv := reflect.ValueOf(want)
	rt := rv.Type()
	if rt.NumField() == 0 {
		t.Fatal("wbStateFile has no fields — this guard is vacuous")
	}
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		wantField := rv.Field(i).Interface()
		gotField := reflect.ValueOf(got).Field(i).Interface()
		if !reflect.DeepEqual(wantField, gotField) {
			t.Errorf("loadState dropped or altered %s:\n  want %#v\n  got  %#v\n"+
				"Every top-level field of wbStateFile must be copied in loadState().", name, wantField, gotField)
		}
	}

	// The copy must be independent: mutating it must not corrupt the cache.
	got.TrialClaimed["auth-three"] = true
	got.Realm[realmGlobal] = mmSettings{Disabled: []string{"mutated"}}
	got.DailyReport.Rows["auth-nine"] = persistedDailyRow{Day: "2026-01-01"}
	*got.CacheTrace = false
	again, err := loadState()
	if err != nil {
		t.Fatalf("loadState (second call): %v", err)
	}
	if again.TrialClaimed["auth-three"] {
		t.Error("loadState returned a live reference to TrialClaimed: mutating it changed the cache")
	}
	if got, want := again.Realm[realmGlobal].Disabled, []string{"gpt-6-sol"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loadState returned a live reference to Realm settings: mutating it changed the cache (got %v, want %v)", got, want)
	}
	if _, ok := again.DailyReport.Rows["auth-nine"]; ok {
		t.Error("loadState returned a live reference to DailyReport.Rows: mutating it changed the cache")
	}
	if again.CacheTrace == nil || !*again.CacheTrace {
		t.Error("loadState returned a live reference to CacheTrace: mutating it changed the cache")
	}
}
