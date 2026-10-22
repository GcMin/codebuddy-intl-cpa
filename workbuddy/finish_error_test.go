package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// finishErrorTransport serves one fixed SSE body for any request.
func finishErrorTransport(body string) func() {
	return setDirectTransport(executorErrorTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))
}

// captureEmits overrides the host stream sink and returns the emitted payloads.
func captureEmits(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := streamEmitSource
	streamEmitSource = func(_ string, payload []byte) error {
		got = append(got, string(payload))
		return nil
	}
	t.Cleanup(func() { streamEmitSource = prev })
	return &got
}

func TestInspectStreamChunk(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		payload  bool
		roleOnly bool
		finish   string
	}{
		{"role opener", `{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, false, true, ""},
		{"content", `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`, true, false, ""},
		{"reasoning", `{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`, true, false, ""},
		{"tool call", `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0}]}}]}`, true, false, ""},
		{"terminal stop", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, false, false, "stop"},
		{"terminal error", `{"choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`, false, false, "error"},
		{"usage only", `{"choices":[],"usage":{"total_tokens":3}}`, false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := inspectStreamChunk(tc.in)
			if v.payload != tc.payload || v.roleOnly != tc.roleOnly || v.finish != tc.finish {
				t.Fatalf("verdict = %+v, want payload=%v roleOnly=%v finish=%q", v, tc.payload, tc.roleOnly, tc.finish)
			}
		})
	}
}

// CB-053: a finish_reason "error" terminal that arrives before any content must
// fail the attempt without emitting anything, so the failover loop can retry.
func TestPumpAttemptFinishErrorBeforeContent(t *testing.T) {
	const body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"error\"}]}\n\ndata: [DONE]\n\n"
	restore := finishErrorTransport(body)
	t.Cleanup(restore)
	emits := captureEmits(t)

	req, err := http.NewRequest(http.MethodPost, "https://www.codebuddy.ai/v2/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	emitted, status, err := pumpAttempt(req, "stream-1", false, "deepseek-v4.1-flash", "deepseek-v4.1-flash", "idx", "uid", time.Now(), &sseUsageCollector{})
	if !isUpstreamFinishError(err) {
		t.Fatalf("err = %v, want errUpstreamFinishError", err)
	}
	if emitted || status != 0 {
		t.Fatalf("emitted/status = %v/%d, want false/0", emitted, status)
	}
	if len(*emits) != 0 {
		t.Fatalf("emits = %v, want none (role-only opener must be held)", *emits)
	}
}

// A rejection that arrives after content was delivered cannot be retried; the
// attempt must still be reported as failed instead of a zero-usage success.
func TestPumpAttemptFinishErrorAfterContent(t *testing.T) {
	const body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"error\"}]}\n\n" +
		"data: [DONE]\n\n"
	restore := finishErrorTransport(body)
	t.Cleanup(restore)
	emits := captureEmits(t)

	req, err := http.NewRequest(http.MethodPost, "https://www.codebuddy.ai/v2/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	emitted, _, err := pumpAttempt(req, "stream-2", false, "deepseek-v4.1-flash", "deepseek-v4.1-flash", "idx", "uid", time.Now(), &sseUsageCollector{})
	if !isUpstreamFinishError(err) {
		t.Fatalf("err = %v, want errUpstreamFinishError", err)
	}
	if !emitted {
		t.Fatal("emitted = false, want true (content already reached the client)")
	}
	if len(*emits) != 2 {
		t.Fatalf("emits = %d (%v), want held role + content", len(*emits), *emits)
	}
	for _, e := range *emits {
		if strings.Contains(e, `"finish_reason":"error"`) {
			t.Fatalf("error terminal was forwarded to the client: %s", e)
		}
	}
}

// The held role-only opener must still be flushed on a normal stream.
func TestPumpAttemptHeldRoleFlushedOnContent(t *testing.T) {
	const body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	restore := finishErrorTransport(body)
	t.Cleanup(restore)
	emits := captureEmits(t)

	req, err := http.NewRequest(http.MethodPost, "https://www.codebuddy.ai/v2/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	emitted, status, err := pumpAttempt(req, "stream-3", false, "deepseek-v4.1-flash", "deepseek-v4.1-flash", "idx", "uid", time.Now(), &sseUsageCollector{})
	if err != nil || !emitted || status != 0 {
		t.Fatalf("emitted/status/err = %v/%d/%v", emitted, status, err)
	}
	if len(*emits) != 3 {
		t.Fatalf("emits = %d (%v), want role, content, stop", len(*emits), *emits)
	}
	if !strings.Contains((*emits)[0], `"role":"assistant"`) {
		t.Fatalf("first emit = %s, want the held role opener", (*emits)[0])
	}
	if !strings.Contains((*emits)[2], `"finish_reason":"stop"`) {
		t.Fatalf("last emit = %s, want the stop terminal", (*emits)[2])
	}
}

// The folded (non-stream) path must not turn the error terminal into a
// completion with finish_reason "error".
func TestAggregateCompletionCountedFinishError(t *testing.T) {
	body := "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"error\"}]}\n\n" +
		"data: [DONE]\n\n"
	out, sawData, err := aggregateCompletionCounted(strings.NewReader(body), "m")
	if !isUpstreamFinishError(err) {
		t.Fatalf("err = %v, want errUpstreamFinishError", err)
	}
	if out != nil || !sawData {
		t.Fatalf("out/sawData = %v/%v, want nil/true", out, sawData)
	}
}

// CB-053: retry a pre-content finish error once on the next account.
func TestRunStreamFailover_RetriesFinishErrorOnce(t *testing.T) {
	resetAuthCooldown("idx-1")
	resetAuthCooldown("idx-2")
	t.Cleanup(func() { resetAuthCooldown("idx-1"); resetAuthCooldown("idx-2") })

	cands := []*failoverCandidate{
		{AuthID: "idx-1", Auth: &storedAuth{}},
		{AuthID: "idx-2", Auth: &storedAuth{}},
	}
	var order []string
	fin := runStreamFailover(cands, func(cand *failoverCandidate) (bool, int, error) {
		order = append(order, cand.AuthID)
		if cand.AuthID == "idx-1" {
			return false, 0, errUpstreamFinishError
		}
		return false, 0, nil
	})
	if fin.Outcome != outcomeSuccess || fin.AuthID != "idx-2" {
		t.Fatalf("fin = %+v, want success on idx-2", fin)
	}
	if len(order) != 2 || order[0] != "idx-1" || order[1] != "idx-2" {
		t.Fatalf("attempt order = %v, want [idx-1 idx-2]", order)
	}
	if isAuthOnCooldown("idx-1") {
		t.Fatal("a finish_reason error must not quarantine the account")
	}
}

// Only one retry: a second finish error stops the loop and surfaces the error.
func TestRunStreamFailover_StopsAfterOneFinishErrorRetry(t *testing.T) {
	resetAuthCooldown("idx-1")
	resetAuthCooldown("idx-2")
	t.Cleanup(func() { resetAuthCooldown("idx-1"); resetAuthCooldown("idx-2") })

	cands := []*failoverCandidate{
		{AuthID: "idx-1", Auth: &storedAuth{}},
		{AuthID: "idx-2", Auth: &storedAuth{}},
	}
	var order []string
	fin := runStreamFailover(cands, func(cand *failoverCandidate) (bool, int, error) {
		order = append(order, cand.AuthID)
		return false, 0, errUpstreamFinishError
	})
	if fin.Outcome != outcomeStop || !isUpstreamFinishError(fin.Err) {
		t.Fatalf("fin = %+v, want stop with the finish error surfaced", fin)
	}
	if len(order) != 2 {
		t.Fatalf("attempt order = %v, want exactly one retry (2 attempts)", order)
	}
	if isAuthOnCooldown("idx-1") || isAuthOnCooldown("idx-2") {
		t.Fatal("finish_reason errors must not quarantine accounts")
	}
}

// The non-stream / sync-collected loop gets the same single retry.
func TestRunFailover_RetriesFinishErrorOnce(t *testing.T) {
	resetAuthCooldown("idx-1")
	resetAuthCooldown("idx-2")
	t.Cleanup(func() { resetAuthCooldown("idx-1"); resetAuthCooldown("idx-2") })

	cands := []*failoverCandidate{
		{AuthID: "idx-1", Auth: &storedAuth{}},
		{AuthID: "idx-2", Auth: &storedAuth{}},
	}
	var order []string
	fin := runFailover(cands, func(cand *failoverCandidate) (attemptOutcome, int, string, error) {
		order = append(order, cand.AuthID)
		if cand.AuthID == "idx-1" {
			return classifyNonStream(nil, errUpstreamFinishError)
		}
		return outcomeSuccess, 0, "", nil
	})
	if fin.Outcome != outcomeSuccess || fin.AuthID != "idx-2" {
		t.Fatalf("fin = %+v, want success on idx-2", fin)
	}
	if len(order) != 2 {
		t.Fatalf("attempt order = %v, want [idx-1 idx-2]", order)
	}
	if isAuthOnCooldown("idx-1") {
		t.Fatal("a finish_reason error must not quarantine the account")
	}
}

func TestIsUpstreamFinishError(t *testing.T) {
	if !isUpstreamFinishError(errUpstreamFinishError) {
		t.Fatal("sentinel must be recognised directly")
	}
	if isUpstreamFinishError(errEmptyStream) {
		t.Fatal("empty_stream must not be confused with the finish error")
	}
	if isUpstreamFinishError(errors.New("other")) {
		t.Fatal("unrelated errors must not match")
	}
}

// A single-account pool has no second candidate to retry on. The loop must
// surface the rejection it saw, not rewrite it into 429 "all accounts
// unavailable" (unavailableFailoverResult does that when res.Err is unset).
func TestRunStreamFailover_SingleCandidateKeepsFinishError(t *testing.T) {
	resetAuthCooldown("idx-1")
	t.Cleanup(func() { resetAuthCooldown("idx-1") })

	fin := runStreamFailover([]*failoverCandidate{{AuthID: "idx-1", Auth: &storedAuth{}}},
		func(*failoverCandidate) (bool, int, error) { return false, 0, errUpstreamFinishError })
	if fin.Outcome != outcomeStop || !isUpstreamFinishError(fin.Err) {
		t.Fatalf("fin = %+v, want outcomeStop with the finish error", fin)
	}
	if fin.Status == http.StatusTooManyRequests {
		t.Fatal("finish error was rewritten into a 429")
	}
}

func TestRunFailover_SingleCandidateKeepsFinishError(t *testing.T) {
	resetAuthCooldown("idx-1")
	t.Cleanup(func() { resetAuthCooldown("idx-1") })

	fin := runFailover([]*failoverCandidate{{AuthID: "idx-1", Auth: &storedAuth{}}},
		func(*failoverCandidate) (attemptOutcome, int, string, error) {
			return classifyNonStream(nil, errUpstreamFinishError)
		})
	if fin.Outcome != outcomeStop || !isUpstreamFinishError(fin.Err) {
		t.Fatalf("fin = %+v, want outcomeStop with the finish error", fin)
	}
	if fin.Status == http.StatusTooManyRequests {
		t.Fatal("finish error was rewritten into a 429")
	}
}
