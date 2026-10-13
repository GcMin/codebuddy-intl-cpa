package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func resetChatSessions(t *testing.T) {
	t.Helper()
	chatSessions.mu.Lock()
	old, version := chatSessions.entries, chatSessions.version
	chatSessions.entries = make(map[string]sessionBinding)
	chatSessions.version = 0
	chatSessions.mu.Unlock()
	t.Cleanup(func() {
		chatSessions.mu.Lock()
		chatSessions.entries, chatSessions.version = old, version
		chatSessions.mu.Unlock()
	})
}

func TestRequestSessionKeyScopedStableAndNoRawValues(t *testing.T) {
	req := pluginapi.ExecutorRequest{Model: "cb/gpt-6-sol", Headers: http.Header{"Authorization": []string{"Bearer fake-private-client-key"}}}
	body := []byte(`{"messages":[{"role":"system","content":"instructions"},{"role":"user","content":"first turn"}]}`)
	appended := []byte(`{"messages":[{"role":"system","content":"instructions"},{"role":"user","content":"first turn"},{"role":"assistant","content":"reply"},{"role":"user","content":"next"}]}`)
	key := requestSessionKey(req, body, realmGlobal)
	if len(key) != 64 || key != requestSessionKey(req, appended, realmGlobal) || strings.Contains(key, "fake-private") || strings.Contains(key, "first turn") {
		t.Fatalf("unstable or unsafe session key (length=%d)", len(key))
	}
	other := req
	other.Headers = http.Header{"Authorization": []string{"Bearer another-client"}}
	if key == requestSessionKey(other, body, realmGlobal) || key == requestSessionKey(req, body, realmCN) {
		t.Fatal("client identity or realm isolation missing")
	}
	other = req
	other.Model = "cb/gpt-6.1-sol"
	if key == requestSessionKey(other, body, realmGlobal) {
		t.Fatal("model isolation missing")
	}
	req.Headers = nil
	if got := requestSessionKey(req, body, realmGlobal); got != "" {
		t.Fatal("unscoped identical prompts must not share account bindings")
	}
}

func TestSoftStickyPrefersSameAccountThenRebindsOnBusy(t *testing.T) {
	resetChatSessions(t)
	cands := exhaustedCandidates(t)
	key := "session:soft-sticky-test"
	attempt := func(c *failoverCandidate) (attemptOutcome, int, string, error) { return outcomeSuccess, 200, "", nil }
	first := runFailoverForSession(cands, key, attempt)
	if first.AuthID != cands[0].AuthID {
		t.Fatalf("first selection: %+v", first)
	}
	// Host may route B for the next turn. The plugin affinity must prefer A.
	reversed := []*failoverCandidate{cands[1], cands[0]}
	second := runFailoverForSession(reversed, key, attempt)
	if second.AuthID != cands[0].AuthID {
		t.Fatalf("session did not stay on A: %+v", second)
	}
	one, err := admitCandidateAttempt(context.Background(), cands[0])
	if err != nil || one == nil {
		t.Fatal(err)
	}
	two, err := admitCandidateAttempt(context.Background(), cands[0])
	if err != nil || two == nil {
		one()
		t.Fatal(err)
	}
	defer one()
	defer two()
	started := time.Now()
	overflow := runFailoverForSession(cands, key, attempt)
	if overflow.AuthID != cands[1].AuthID || time.Since(started) > time.Second {
		t.Fatalf("busy sticky account blocked instead of using peer: %+v", overflow)
	}
	// Even after A becomes free, the new mapping stays on B (no ping-pong).
	two()
	one()
	following := runFailoverForSession(cands, key, attempt)
	if following.AuthID != cands[1].AuthID {
		t.Fatalf("session bounced back to old account: %+v", following)
	}
	otherSession := runFailoverForSession(cands, "session:other", attempt)
	if otherSession.AuthID != cands[0].AuthID {
		t.Fatalf("affinity leaked across sessions: %+v", otherSession)
	}
}

func TestSessionBindingVisibleDuringInFlightAndOlderCompletionCannotUndoOverflow(t *testing.T) {
	resetChatSessions(t)
	cands := exhaustedCandidates(t)
	key := "session:overlap"
	entered := make(chan struct{})
	resume := make(chan struct{})
	finished := make(chan failoverResult, 1)
	go func() {
		finished <- runFailoverForSession(cands, key, func(c *failoverCandidate) (attemptOutcome, int, string, error) {
			close(entered)
			<-resume
			return outcomeSuccess, 200, "", nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter")
	}
	if got := runFailoverForSession([]*failoverCandidate{cands[1], cands[0]}, key, func(*failoverCandidate) (attemptOutcome, int, string, error) { return outcomeSuccess, 200, "", nil }); got.AuthID != cands[0].AuthID {
		t.Fatalf("in-flight session not yet bound: %+v", got)
	}
	// A has one occupied slot. Fill the other, then overflow to B.
	extra, err := admitCandidateAttempt(context.Background(), cands[0])
	if err != nil || extra == nil {
		t.Fatal(err)
	}
	if got := runFailoverForSession(cands, key, func(*failoverCandidate) (attemptOutcome, int, string, error) { return outcomeSuccess, 200, "", nil }); got.AuthID != cands[1].AuthID {
		t.Fatalf("overflow did not rebind: %+v", got)
	}
	extra()
	close(resume)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("first attempt stuck")
	}
	if got := runFailoverForSession(cands, key, func(*failoverCandidate) (attemptOutcome, int, string, error) { return outcomeSuccess, 200, "", nil }); got.AuthID != cands[1].AuthID {
		t.Fatalf("older completion reverted new binding: %+v", got)
	}
}

func TestFailedAttemptDoesNotPinSessionAndStreamPartialRetainsIt(t *testing.T) {
	resetChatSessions(t)
	cands := exhaustedCandidates(t)
	key := "session:failure"
	failure := runFailoverForSession(cands, key, func(*failoverCandidate) (attemptOutcome, int, string, error) {
		return outcomeStop, 500, "upstream error", errors.New("upstream error")
	})
	if failure.AuthID != cands[0].AuthID {
		t.Fatal(failure)
	}
	if got := chatSessions.preferred(key, []*failoverCandidate{cands[1], cands[0]}); got[0] != cands[1] {
		t.Fatal("failed attempt pinned session")
	}
	partial := runStreamFailoverForSession([]*failoverCandidate{cands[1], cands[0]}, key, func(*failoverCandidate) (bool, int, error) { return true, 200, errors.New("read error") })
	if !partial.Emitted {
		t.Fatal(partial)
	}
	if got := chatSessions.preferred(key, cands); got[0] != cands[1] {
		t.Fatal("partial stream was rebound despite emitted chunks")
	}
	// A missing mapped candidate never resurrects a disabled auth.
	if got := chatSessions.preferred(key, []*failoverCandidate{cands[0]}); got[0] != cands[0] {
		t.Fatal("stale binding invented a candidate")
	}
}

func TestSessionBindingExpiryAndCap(t *testing.T) {
	s := sessionBindingStore{entries: make(map[string]sessionBinding)}
	if v := s.begin("old", "global:uid-a"); v == 0 {
		t.Fatal("no binding")
	}
	s.mu.Lock()
	e := s.entries["old"]
	e.expires = time.Now().Add(-time.Second)
	s.entries["old"] = e
	s.mu.Unlock()
	if got := s.preferred("old", exhaustedCandidates(t)); len(got) != 2 {
		t.Fatal("bad preference")
	}
	s.mu.Lock()
	_, ok := s.entries["old"]
	s.mu.Unlock()
	if ok {
		t.Fatal("expired binding retained")
	}
	for i := 0; i < maxSessionBindings+4; i++ {
		s.begin(string(rune(0x1000+i)), "global:uid-a")
	}
	s.mu.Lock()
	size := len(s.entries)
	s.mu.Unlock()
	if size > maxSessionBindings {
		t.Fatalf("unbounded session map: %d", size)
	}
}
