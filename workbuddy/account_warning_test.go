package main

import (
	"strings"
	"testing"
	"time"
)

func TestIsRiskControlResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"code 11140 as the CN account saw it", 403, `{"code":11140,"msg":"request illegal","displayMsg":{"en":"Your account has been restricted due to unusual activity"}}`, true},
		{"code 11140 without wording", 403, `{"code":11140,"msg":""}`, true},
		{"wording without code", 403, `{"msg":"Your account has been restricted due to unusual activity"}`, true},
		{"credit exhaustion is not risk control", 403, `{"code":14018,"msg":"Credits exhausted"}`, false},
		{"rate limit is not risk control", 429, `{"code":6004,"msg":"frequency limit"}`, false},
		{"channel mismatch is not an account block", 400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, false},
		{"success is never a block", 200, `{"code":11140}`, false},
		{"empty body", 403, ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRiskControlResponse(tc.status, tc.body); got != tc.want {
				t.Errorf("isRiskControlResponse(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestClassifyAccountFailure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   attemptOutcome
	}{
		{"risk control fails over", 403, `{"code":11140,"msg":"request illegal"}`, outcomeRateLimited},
		{"exhaustion is its own outcome", 403, `{"code":14018,"msg":"Credits exhausted"}`, outcomeAccountExhausted},
		{"rate limit fails over", 429, `{"code":6004}`, outcomeRateLimited},
		{"plain upstream error stops", 400, `{"code":1234,"msg":"bad request"}`, outcomeStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAccountFailure(tc.status, tc.body); got != tc.want {
				t.Errorf("classifyAccountFailure(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestAccountRiskWarningPersistsAndClears(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())

	keys := []string{"auth-index-1", "auth-id-1", "uid-1"}
	markAccountRiskWarning(keys, `{"code":11140,"msg":"request illegal","displayMsg":{"en":"restricted due to unusual activity"}}`)

	// Lookup works through any of the account's identities, and through the
	// state file rather than only the in-process copy.
	for _, key := range keys {
		w, ok := accountWarningFor(key)
		if !ok {
			t.Fatalf("accountWarningFor(%q) = not found, want a warning", key)
		}
		if w.Code != riskControlCode {
			t.Errorf("warning code = %q, want %q", w.Code, riskControlCode)
		}
		if w.Reason != "unusual activity" {
			t.Errorf("warning reason = %q, want unusual activity", w.Reason)
		}
		if w.Since == "" {
			t.Error("warning must record when it was first seen")
		}
	}

	st, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(st.Warnings) != len(keys) {
		t.Fatalf("persisted warnings = %d, want %d", len(st.Warnings), len(keys))
	}

	// The account must also be parked on the long cooldown so routing skips it.
	if !isAuthOnCooldown("auth-index-1") {
		t.Error("flagged account must be on cooldown")
	}

	clearAccountRiskWarning(keys...)
	if _, ok := accountWarningFor(keys...); ok {
		t.Error("warning survived clear")
	}
	if isAuthOnCooldown("auth-index-1") {
		t.Error("cooldown survived clear")
	}
}

func TestRiskWarningKeepsFirstSighting(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())

	markAccountRiskWarning([]string{"keep-1"}, `{"code":11140,"msg":"restricted due to unusual activity"}`)
	first, _ := accountWarningFor("keep-1")
	// A second sighting must not move the timestamp: it dates the block.
	time.Sleep(10 * time.Millisecond)
	markAccountRiskWarning([]string{"keep-1"}, `{"code":11140,"msg":"restricted due to safety review"}`)
	again, _ := accountWarningFor("keep-1")
	if again.Since != first.Since {
		t.Errorf("Since changed on repeat sighting: %q -> %q", first.Since, again.Since)
	}
	if again.Reason != first.Reason {
		t.Errorf("Reason changed on repeat sighting: %q -> %q", first.Reason, again.Reason)
	}
}

func TestMarkCandidateUnavailableFlagsRiskControl(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
	t.Cleanup(func() { resetAuthCooldown("risk-auth"); resetAuthCooldown("risk-record") })

	cand := &failoverCandidate{AuthID: "risk-auth", RecordID: "risk-record"}
	body := `{"code":11140,"msg":"request illegal"}`
	markCandidateUnavailable(cand, outcomeRateLimited, body)

	w, ok := accountWarningFor("risk-auth")
	if !ok {
		t.Fatal("markCandidateUnavailable did not flag the account")
	}
	if !strings.Contains(w.Reason, "upstream refused") {
		t.Errorf("reason = %q, want the refusal fallback", w.Reason)
	}
	// A risk-control account gets the long window, not the 10-minute 429 cap.
	if _, ok := accountWarningFor("risk-record"); !ok {
		t.Error("warning must be recorded under every candidate identity")
	}
}
