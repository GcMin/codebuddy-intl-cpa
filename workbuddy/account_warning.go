// Account risk-control warnings (upstream chat restriction, code 11140).
//
// The upstream can block an account from *generating* — "request illegal" /
// "account has been restricted due to unusual activity" — while billing,
// check-in and the OAuth endpoints keep answering normally for the same token.
// The credential is valid; the account simply cannot chat, and no retry, token
// refresh, re-import or re-enable fixes that. The state therefore has to be
// visible: without it the panel shows "Active" while every request routed to
// that account fails.
//
// A warning is recorded when the code is first seen, persisted in
// workbuddy-state.json so it survives restarts (the failover cooldown is
// process-local), and parks the account on a long cooldown so routing stops
// selecting it. The operator clears it once the account was checked upstream or
// replaced.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	// riskControlCode is the upstream code for "this account may not generate".
	// Distinct from 11128 (channel/prompt mismatch) and 14018 (credits).
	riskControlCode = "11140"
	// riskControlCooldown keeps a flagged account out of routing. Unlike a 429
	// it does not recover on its own, so the window is long and is meant to be
	// cleared explicitly after the account is verified or replaced.
	riskControlCooldown = 6 * time.Hour
)

// riskControlCodeRe matches the code inside an upstream envelope. It is matched
// on the body because the failure arrives as HTTP 403 and the code is the only
// stable field — the human message is localized.
var riskControlCodeRe = regexp.MustCompile(`"code"\s*:\s*11140\b`)

// accountWarning is the panel-facing record of a flagged account.
type accountWarning struct {
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
	Since  string `json:"since"`
}

// isRiskControlResponse reports whether an upstream failure is the account-level
// chat restriction rather than a credit or rate-limit problem. A status code of
// 0 means "unknown, judge the body alone" (the failover helpers only carry the
// body in places).
func isRiskControlResponse(statusCode int, body string) bool {
	if statusCode > 0 && statusCode < 400 {
		return false
	}
	if strings.TrimSpace(body) == "" {
		return false
	}
	if riskControlCodeRe.MatchString(body) {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "been restricted") ||
		strings.Contains(lower, "restricted due to unusual activity")
}

// riskControlReason turns the upstream message into a short panel reason.
func riskControlReason(body string) string {
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "unusual activity"):
		return "unusual activity"
	case strings.Contains(lower, "safety review"):
		return "safety review"
	}
	return "upstream refused the account"
}

// markAccountRiskWarning records the warning under every key the account is
// known by and parks it on a long cooldown. The state write is best effort; the
// in-process cooldown still protects traffic if the disk write fails.
func markAccountRiskWarning(keys []string, body string) {
	keys = uniqueNonEmptyKeys(keys)
	if len(keys) == 0 {
		return
	}
	w := accountWarning{
		Code:   riskControlCode,
		Reason: riskControlReason(body),
		Since:  time.Now().UTC().Format(time.RFC3339),
	}
	_ = mutateState(func(st *wbStateFile) error {
		if st.Warnings == nil {
			st.Warnings = map[string]accountWarning{}
		}
		for _, k := range keys {
			if old, ok := st.Warnings[k]; ok && old.Since != "" {
				continue // keep the first sighting: it dates the block
			}
			st.Warnings[k] = w
		}
		return nil
	})
	until := time.Now().Add(riskControlCooldown)
	for _, k := range keys {
		markAuthCooldownReason(k, until, "risk_control")
	}
	fmt.Fprintf(os.Stderr, "[workbuddy] risk-control: auth=%q flagged reason=%q until=%s\n",
		redactSecrets(keys[0]), w.Reason, until.UTC().Format(time.RFC3339))
}

// markCandidateRiskBlocked is the failover-loop entry point: it flags the
// candidate under every identity it is reachable by.
func markCandidateRiskBlocked(c *failoverCandidate, body string) {
	if c == nil {
		return
	}
	markAccountRiskWarning(candidateCooldownIDs(c), body)
}

// accountWarningFor returns the warning for any of the account's keys.
func accountWarningFor(keys ...string) (accountWarning, bool) {
	st, err := loadState()
	if err != nil || st.Warnings == nil {
		return accountWarning{}, false
	}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if w, ok := st.Warnings[k]; ok {
			return w, true
		}
	}
	return accountWarning{}, false
}

// clearAccountRiskWarning removes the flag and its cooldown — used after the
// operator checked the account upstream or swapped it out.
func clearAccountRiskWarning(keys ...string) {
	keys = uniqueNonEmptyKeys(keys)
	if len(keys) == 0 {
		return
	}
	_ = mutateState(func(st *wbStateFile) error {
		for _, k := range keys {
			delete(st.Warnings, k)
		}
		return nil
	})
	for _, k := range keys {
		resetAuthCooldown(k)
	}
}

// uniqueNonEmptyKeys trims, drops empties and de-duplicates while keeping order.
func uniqueNonEmptyKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// classifyAccountFailure maps an upstream failure to the failover outcome used
// by every attempt loop, so the three loops cannot drift apart. Risk control is
// treated as "skip this account, try the next one" (like a rate limit), while
// markCandidateUnavailable gives it its own long cooldown and warning.
func classifyAccountFailure(status int, body string) attemptOutcome {
	switch {
	case isAccountExhaustedResponse(status, body):
		return outcomeAccountExhausted
	case isRiskControlResponse(status, body):
		return outcomeRateLimited
	case upstreamErrorCode(body) == modelNotAllowedCode:
		return outcomeModelNotAllowed
	case isRateLimitResponse(status, body):
		return outcomeRateLimited
	}
	return outcomeStop
}
