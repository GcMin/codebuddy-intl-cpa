package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCleanChunkJSON_NormalizesEmptyFinishReason(t *testing.T) {
	// The first upstream frame carries role/content plus an invalid empty
	// finish_reason — the GH#13 first-frame break for strict clients.
	in := `{"choices":[{"delta":{"content":"","role":"assistant"},"finish_reason":"","index":0}],"id":"c1","model":"m"}`
	out := cleanChunkJSON(in)
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ch := obj["choices"].([]any)[0].(map[string]any)
	fr, present := ch["finish_reason"]
	if !present {
		t.Fatal("finish_reason dropped; want null")
	}
	if fr != nil {
		t.Fatalf("finish_reason = %v, want null", fr)
	}
	if ch["delta"].(map[string]any)["role"] != "assistant" {
		t.Fatal("role lost")
	}

	// Real finish values must pass untouched.
	keep := `{"choices":[{"delta":{},"finish_reason":"stop","index":0}]}`
	if out := cleanChunkJSON(keep); !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("stop finish_reason mangled: %s", out)
	}

	// A delta that folds to {} with an empty finish_reason is still dropped
	// (nothing of value), and now also with the normalized null.
	shell := `{"choices":[{"delta":{"function_call":null},"finish_reason":"","index":0}]}`
	if out := cleanChunkJSON(shell); out != "" {
		t.Fatalf("noop shell should be dropped, got %s", out)
	}

	// Choices WITHOUT a delta map are covered too: normalization runs before
	// the delta extraction (GH#13 follow-up — reporter-suggested placement).
	noDelta := `{"choices":[{"index":0,"finish_reason":""}],"id":"c2","model":"m"}`
	out2 := cleanChunkJSON(noDelta)
	var obj2 map[string]any
	if err := json.Unmarshal([]byte(out2), &obj2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ch2 := obj2["choices"].([]any)[0].(map[string]any)
	if ch2["finish_reason"] != nil {
		t.Fatalf("no-delta finish_reason = %v, want null", ch2["finish_reason"])
	}
	deltaNull := `{"choices":[{"delta":null,"finish_reason":"","index":0}]}`
	out3 := cleanChunkJSON(deltaNull)
	if !strings.Contains(out3, `"finish_reason":null`) {
		t.Fatalf("null-delta finish_reason not normalized: %s", out3)
	}
}

func TestUsageTerminatorChunk(t *testing.T) {
	t.Setenv("WB_STREAM_USAGE_TERMINATOR", "")
	c := &sseUsageCollector{
		last:    map[string]any{"prompt_tokens": float64(13), "completion_tokens": float64(1), "total_tokens": float64(14)},
		respID:  "id-1",
		created: 1700000000,
		model:   "deepseek-v4.1-flash",
	}
	raw := usageTerminatorChunk(c, "cb/deepseek-v4.1-flash", "deepseek-v4.1-flash")
	if raw == nil {
		t.Fatal("terminator missing")
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frame["choices"].([]any)) != 0 {
		t.Fatalf("choices = %v, want []", frame["choices"])
	}
	u := frame["usage"].(map[string]any)
	if u["total_tokens"] != float64(14) {
		t.Fatalf("usage = %v", u)
	}
	if frame["id"] != "id-1" || frame["model"] != "deepseek-v4.1-flash" || frame["created"] != float64(1700000000) {
		t.Fatalf("identity fields: %v", frame)
	}
	if frame["object"] != "chat.completion.chunk" {
		t.Fatalf("object = %v", frame["object"])
	}

	// No usage observed -> no frame.
	if got := usageTerminatorChunk(&sseUsageCollector{}, "m", "m"); got != nil {
		t.Fatalf("no-usage terminator = %s, want nil", got)
	}

	// Field rollback switch.
	t.Setenv("WB_STREAM_USAGE_TERMINATOR", "off")
	if got := usageTerminatorChunk(c, "m", "m"); got != nil {
		t.Fatalf("kill switch ignored: %s", got)
	}
	t.Setenv("WB_STREAM_USAGE_TERMINATOR", "")
	_ = time.Now
}
