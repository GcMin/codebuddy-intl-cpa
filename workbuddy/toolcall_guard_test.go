package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// captureGuardLog swaps the guard log writer for a buffer for the duration of
// one test and returns the captured output.
func captureGuardLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	guardLogWriter = buf
	t.Cleanup(func() { guardLogWriter = os.Stderr })
	return buf
}

// toolCallDelta builds one streaming tool_call fragment.
func toolCallDelta(index int, id, name, args string) map[string]any {
	call := map[string]any{"index": index}
	if id != "" {
		call["id"] = id
	}
	fn := map[string]any{}
	if name != "" {
		call["type"] = "function"
		fn["name"] = name
	}
	if args != "" {
		fn["arguments"] = args
	}
	if len(fn) > 0 {
		call["function"] = fn
	}
	return call
}

// sseFromToolCallDeltas renders deltas as the upstream SSE frames the plugin
// folds (one data event per delta, terminated by [DONE]).
func sseFromToolCallDeltas(deltas []map[string]any) string {
	var b strings.Builder
	for _, delta := range deltas {
		chunk := map[string]any{
			"id":      "chatcmpl-guard-test",
			"object":  "chat.completion.chunk",
			"model":   "glm-5.3",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{delta}}}},
		}
		raw, _ := json.Marshal(chunk)
		b.WriteString("data: ")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// foldedToolCalls parses the tool_calls array of a folded completion.
func foldedToolCalls(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var completion map[string]any
	if err := json.Unmarshal(out, &completion); err != nil {
		t.Fatalf("folded completion is not valid JSON: %v", err)
	}
	choices, _ := completion["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("want 1 choice, got %d", len(choices))
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	raw, _ := message["tool_calls"].([]any)
	calls := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		call, _ := r.(map[string]any)
		calls = append(calls, call)
	}
	return calls
}

// TestToolcallGuard_RealWorld1014EchoSkip replays the production runaway loop:
// 1.014 tool calls of the same "echo skip" call in a single completion. The
// guard must reject the completion with a clean error (no payload handed back,
// no silent truncation) and log the trip.
func TestToolcallGuard_RealWorld1014EchoSkip(t *testing.T) {
	log := captureGuardLog(t)
	deltas := make([]map[string]any, 0, 1014)
	for i := 0; i < 1014; i++ {
		// Upstream stamps each streamed call with its own id, so these are
		// distinct calls that merely repeat the same tool and arguments.
		deltas = append(deltas, toolCallDelta(i, fmt.Sprintf("call_echo_%d", i), "echo", `{"text":"skip"}`))
	}
	out, sawData, err := aggregateCompletionCounted(strings.NewReader(sseFromToolCallDeltas(deltas)), "glm-5.3")
	if err == nil {
		t.Fatalf("guard did not trip on 1014 tool calls (out=%d bytes, sawData=%v)", len(out), sawData)
	}
	if out != nil {
		t.Fatalf("guard must not return a payload alongside the error, got %d bytes", len(out))
	}
	if !sawData {
		t.Fatalf("sawData should stay true, the stream did carry chunks")
	}
	if !strings.Contains(err.Error(), "toolcall guard") {
		t.Fatalf("error should name the guard, got: %v", err)
	}
	if !strings.Contains(err.Error(), "1014") {
		t.Fatalf("error should report the offending count, got: %v", err)
	}
	want := "toolcall guard tripped: 1014 folded to 1014"
	if !strings.Contains(log.String(), want) {
		t.Fatalf("log missing %q, got: %q", want, log.String())
	}
}

// TestToolcallGuard_NormalCallsPassThrough is the anti-regression baseline:
// ordinary completions with 1-6 tool calls must survive the guard untouched,
// in order, with their ids/names/arguments intact and no log noise.
func TestToolcallGuard_NormalCallsPassThrough(t *testing.T) {
	for n := 1; n <= 6; n++ {
		t.Run(fmt.Sprintf("%d_calls", n), func(t *testing.T) {
			log := captureGuardLog(t)
			deltas := make([]map[string]any, 0, n)
			for i := 0; i < n; i++ {
				deltas = append(deltas, toolCallDelta(i, fmt.Sprintf("call_%d", i), "read_file", fmt.Sprintf(`{"path":"/tmp/file_%d"}`, i)))
			}
			out, _, err := aggregateCompletionCounted(strings.NewReader(sseFromToolCallDeltas(deltas)), "glm-5.3")
			if err != nil {
				t.Fatalf("guard rejected a normal %d-call completion: %v", n, err)
			}
			calls := foldedToolCalls(t, out)
			if len(calls) != n {
				t.Fatalf("want %d tool calls, got %d", n, len(calls))
			}
			for i, call := range calls {
				if got := call["id"]; got != fmt.Sprintf("call_%d", i) {
					t.Fatalf("call %d id = %v, want call_%d", i, got, i)
				}
				if got := call["index"]; got != float64(i) {
					t.Fatalf("call %d index = %v, want %d", i, got, i)
				}
				fn, _ := call["function"].(map[string]any)
				if fn["name"] != "read_file" {
					t.Fatalf("call %d name = %v", i, fn["name"])
				}
				wantArgs := fmt.Sprintf(`{"path":"/tmp/file_%d"}`, i)
				if fn["arguments"] != wantArgs {
					t.Fatalf("call %d arguments = %v, want %s", i, fn["arguments"], wantArgs)
				}
			}
			if log.Len() != 0 {
				t.Fatalf("guard logged on a clean completion: %q", log.String())
			}
		})
	}
}

// TestToolcallGuard_FragmentMergeMultiIndexNotDeduped covers the dangerous
// regression: calls whose name/arguments arrive as multiple fragments must be
// merged per index, and two calls that end up textually identical but carry
// different ids must both survive.
func TestToolcallGuard_FragmentMergeMultiIndexNotDeduped(t *testing.T) {
	log := captureGuardLog(t)
	deltas := []map[string]any{
		// index 0: id + name split, then arguments split over two frames.
		toolCallDelta(0, "call_a", "ec", ""),
		toolCallDelta(0, "", "ho", ""),
		toolCallDelta(0, "", "", `{"text":`),
		toolCallDelta(0, "", "", `"skip"}`),
		// index 1: same tool + arguments, different id -> not a duplicate.
		toolCallDelta(1, "call_b", "echo", ""),
		toolCallDelta(1, "", "", `{"text":"skip"}`),
	}
	out, _, err := aggregateCompletionCounted(strings.NewReader(sseFromToolCallDeltas(deltas)), "glm-5.3")
	if err != nil {
		t.Fatalf("guard rejected a fragment-merged completion: %v", err)
	}
	calls := foldedToolCalls(t, out)
	if len(calls) != 2 {
		t.Fatalf("fragment merge collapsed wrongly: want 2 calls, got %d", len(calls))
	}
	for i, wantID := range []string{"call_a", "call_b"} {
		if calls[i]["id"] != wantID {
			t.Fatalf("call %d id = %v, want %s", i, calls[i]["id"], wantID)
		}
		fn, _ := calls[i]["function"].(map[string]any)
		if fn["name"] != "echo" {
			t.Fatalf("call %d name = %v, want echo", i, fn["name"])
		}
		if fn["arguments"] != `{"text":"skip"}` {
			t.Fatalf("call %d arguments = %v", i, fn["arguments"])
		}
	}
	if log.Len() != 0 {
		t.Fatalf("guard logged on a fragment-merged completion: %q", log.String())
	}
}

// TestGuardToolCalls_FoldsExactDuplicates pins the dedup contract: exact
// duplicates (id+name+arguments) fold to the first occurrence and the fold is
// logged with the "N folded to M" line.
func TestGuardToolCalls_FoldsExactDuplicates(t *testing.T) {
	log := captureGuardLog(t)
	calls := []map[string]any{
		{"index": 0, "id": "call_dup", "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"text":"skip"}`}},
		{"index": 1, "id": "call_dup", "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"text":"skip"}`}},
		{"index": 2, "id": "call_unique", "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"text":"skip"}`}},
		{"index": 3, "id": "call_dup", "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"text":"skip"}`}},
		{"index": 4, "id": "call_other", "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"text":"stay"}`}},
	}
	got, err := guardToolCalls(calls)
	if err != nil {
		t.Fatalf("fold of 5 calls (3 dups) should not error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 surviving calls, got %d", len(got))
	}
	if got[0]["id"] != "call_dup" || got[1]["id"] != "call_unique" || got[2]["id"] != "call_other" {
		t.Fatalf("dedup changed order or kept the wrong calls: %v", got)
	}
	if !strings.Contains(log.String(), "toolcall guard tripped: 5 folded to 3") {
		t.Fatalf("log missing fold line, got: %q", log.String())
	}
}

// TestGuardToolCalls_CapTripsOnDistinctCalls pins the cap: one call over the
// limit is enough to reject, and the payload is never truncated.
func TestGuardToolCalls_CapTripsOnDistinctCalls(t *testing.T) {
	log := captureGuardLog(t)
	calls := make([]map[string]any, 0, maxToolCallsPerCompletion+1)
	for i := 0; i <= maxToolCallsPerCompletion; i++ {
		calls = append(calls, map[string]any{
			"index":    i,
			"id":       fmt.Sprintf("call_%d", i),
			"type":     "function",
			"function": map[string]any{"name": "echo", "arguments": fmt.Sprintf(`{"text":"skip-%d"}`, i)},
		})
	}
	got, err := guardToolCalls(calls)
	if err == nil {
		t.Fatalf("guard accepted %d calls (limit %d)", len(calls), maxToolCallsPerCompletion)
	}
	if got != nil {
		t.Fatalf("guard returned %d calls alongside the error", len(got))
	}
	want := fmt.Sprintf("toolcall guard tripped: %d folded to %d", len(calls), len(calls))
	if !strings.Contains(log.String(), want) {
		t.Fatalf("log missing %q, got: %q", want, log.String())
	}
}

// TestGuardToolCalls_CapBoundaryKeeps32 locks the boundary: exactly 32 calls is
// still a valid completion.
func TestGuardToolCalls_CapBoundaryKeeps32(t *testing.T) {
	captureGuardLog(t)
	calls := make([]map[string]any, 0, maxToolCallsPerCompletion)
	for i := 0; i < maxToolCallsPerCompletion; i++ {
		calls = append(calls, map[string]any{
			"index":    i,
			"id":       fmt.Sprintf("call_%d", i),
			"type":     "function",
			"function": map[string]any{"name": "echo", "arguments": fmt.Sprintf(`{"text":"skip-%d"}`, i)},
		})
	}
	got, err := guardToolCalls(calls)
	if err != nil {
		t.Fatalf("32 calls must pass the cap: %v", err)
	}
	if len(got) != maxToolCallsPerCompletion {
		t.Fatalf("want %d calls, got %d", maxToolCallsPerCompletion, len(got))
	}
}

// TestGuardToolCalls_IdlessCallsNeverDeduped guards against over-eager dedup:
// without an id there is no proof two calls are the same, so both are kept.
func TestGuardToolCalls_IdlessCallsNeverDeduped(t *testing.T) {
	captureGuardLog(t)
	calls := []map[string]any{
		{"index": 0, "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"text":"skip"}`}},
		{"index": 1, "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"text":"skip"}`}},
	}
	got, err := guardToolCalls(calls)
	if err != nil {
		t.Fatalf("id-less calls should not error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("id-less identical calls were wrongly deduped: got %d", len(got))
	}
}

// TestGuardToolCalls_EmptyAndNil keeps the no-op path safe.
func TestGuardToolCalls_EmptyAndNil(t *testing.T) {
	log := captureGuardLog(t)
	for _, calls := range [][]map[string]any{nil, {}} {
		got, err := guardToolCalls(calls)
		if err != nil {
			t.Fatalf("empty call list errored: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("empty call list returned %d calls", len(got))
		}
	}
	if log.Len() != 0 {
		t.Fatalf("guard logged on an empty completion: %q", log.String())
	}
}

// TestGuardLogWriterDefaultsToStderr documents the production sink.
func TestGuardLogWriterDefaultsToStderr(t *testing.T) {
	if guardLogWriter != os.Stderr {
		t.Fatalf("guard log should default to stderr")
	}
}
