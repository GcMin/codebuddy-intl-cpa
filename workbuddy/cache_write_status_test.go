package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheWritePresenceAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		value  int64
		status string
	}{
		{"missing", nil, 0, "missing"},
		{"zero", map[string]any{"prompt_cache_write_tokens": float64(0)}, 0, "reported"},
		{"positive", map[string]any{"prompt_cache_write_tokens": float64(25)}, 25, "reported"},
		{"alias", map[string]any{"cache_creation_tokens": json.Number("12")}, 12, "reported"},
		{"null", map[string]any{"prompt_cache_write_tokens": nil}, 0, "invalid"},
		{"boolean", map[string]any{"prompt_cache_write_tokens": true}, 0, "invalid"},
		{"fraction", map[string]any{"prompt_cache_write_tokens": 1.5}, 0, "invalid"},
		{"negative", map[string]any{"prompt_cache_write_tokens": int64(-1)}, 0, "invalid"},
		{"text", map[string]any{"prompt_cache_write_tokens": "private-value"}, 0, "invalid"},
		{"overflow", map[string]any{"prompt_cache_write_tokens": math.Pow(2, 63)}, 0, "invalid"},
		{"nan", map[string]any{"prompt_cache_write_tokens": math.NaN()}, 0, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, status := cacheWriteReport(tc.fields)
			if value != tc.value || status != tc.status {
				t.Fatalf("write=%d status=%s", value, status)
			}
			m := map[string]any{"prompt_tokens": float64(100), "completion_tokens": float64(10), "total_tokens": float64(110), "credit": .125}
			for k, v := range tc.fields {
				m[k] = v
			}
			detail, credit := usagePartsFromMap(m)
			if detail.InputTokens != 100 || detail.OutputTokens != 10 || detail.TotalTokens != 110 || credit != .125 || detail.CacheCreationTokens != tc.value {
				t.Fatal("write diagnostics changed total usage or credit")
			}
		})
	}
}

func TestCacheWriteStatusPersistsWithoutRewritingLegacyRows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKBUDDY_DATA_DIR", dir)
	resetUsageMetaCache(t)
	defer stubUsageAuth(t, testAuthList, testAuthGet)()
	old := usageStore
	usageStore = &requestUsageStore{perDay: map[string]map[string]*dayAgg{}}
	t.Cleanup(func() { usageStore = old })
	legacy := []byte(`{"timestamp":"2026-10-28T00:00:00Z","auth_index":"legacy","model":"fixture","input_tokens":100,"cache_write_tokens":0,"credit":2.5}` + "\n")
	path := filepath.Join(dir, usageLogBaseName)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	for i, fields := range []map[string]any{nil, {"prompt_cache_write_tokens": float64(0)}, {"prompt_cache_write_tokens": nil}} {
		m := map[string]any{"prompt_tokens": float64(100), "completion_tokens": float64(10), "total_tokens": float64(110), "credit": .125}
		for k, v := range fields {
			m[k] = v
		}
		recordUsageMap("fixture", "fixture", "auth-alice-id", "uid-alice", time.Now(), m, i == 2)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, legacy) {
		t.Fatal("legacy row was rewritten")
	}
	want := []string{"", "missing", "reported", "invalid"}
	for i, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var r requestUsageRecord
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r.CacheWriteStatus != want[i] || r.CacheWriteTokens != 0 {
			t.Fatalf("row %d status=%s write=%d", i, r.CacheWriteStatus, r.CacheWriteTokens)
		}
	}
	reopened := &requestUsageStore{perDay: map[string]map[string]*dayAgg{}}
	reopened.loadFromDisk()
	rows := reopened.queryRecords("", "", "", 10)
	if len(rows) != 4 {
		t.Fatalf("restored %d records", len(rows))
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.CacheWriteStatus]++
	}
	for _, status := range want {
		if counts[status] != 1 {
			t.Fatalf("lost status %q", status)
		}
	}
}
