package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestUpdateSkipSharedStateConcurrentWithRealmWrites(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
	checker := newUpdateChecker()
	var wg sync.WaitGroup
	for _, realm := range []string{realmGlobal, realmCN} {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				s := defaultSettings(r)
				s.Disabled = []string{fmt.Sprintf("%s-%d", r, i)}
				if err := updateRealmSettings(r, s); err != nil {
					t.Error(err)
				}
			}
		}(realm)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			checker.setSeenVersion(fmt.Sprintf("0.9.%d", i))
		}
	}()
	wg.Wait()
	// Simulate a restart: discard the process cache and use a fresh checker.
	modelsStateMu.Lock()
	modelsStateCache = nil
	modelsStateMu.Unlock()
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{realmGlobal, realmCN} {
		if got := st.Realm[r].Disabled; len(got) != 1 || got[0] != r+"-29" {
			t.Fatalf("lost %s settings: %v", r, got)
		}
	}
	if got := newUpdateChecker().persistedSeen(); got != "0.9.29" {
		t.Fatalf("lost skip marker: %s", got)
	}
}

func TestUpdateSkipMigratesOldRootPathWithoutLosingRealms(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WORKBUDDY_DATA_DIR", "")
	t.Setenv("HOME", root)
	s := defaultSettings(realmGlobal)
	s.Disabled = []string{"keep-model"}
	if err := updateRealmSettings(realmGlobal, s); err != nil {
		t.Fatal(err)
	}
	// Prior updater used root/workbuddy-state.json; MM used .codebuddy_data.
	legacy := filepath.Join(root, mmStateFileName)
	if err := os.WriteFile(legacy, []byte(`{"update":{"seenVersion":"0.9.8"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := updateReadSeenVersion(); got != "0.9.8" {
		t.Fatalf("migration=%q", got)
	}
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Update.SeenVersion != "0.9.8" || len(st.Realm[realmGlobal].Disabled) != 1 {
		t.Fatalf("bad migrated state: %+v", st)
	}
	// The canonical marker wins, even if an old root marker remains.
	updateWriteSeenVersion("0.9.9")
	if got := updateReadSeenVersion(); got != "0.9.9" {
		t.Fatalf("legacy overrides canonical: %s", got)
	}
}

func TestUpdateSkipMigratesLegacyStandalone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKBUDDY_DATA_DIR", dir)
	raw, _ := json.Marshal(updateSeenFile{SeenVersion: "0.9.7"})
	if err := os.WriteFile(filepath.Join(dir, updateFallbackFileName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if got := updateReadSeenVersion(); got != "0.9.7" {
		t.Fatalf("legacy migration=%q", got)
	}
	if _, err := os.Stat(wbStatePath()); err != nil {
		t.Fatal(err)
	}
}
