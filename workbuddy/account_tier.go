// Account plan tier (free vs paid) for model gating.
//
// The upstream gates some models by plan: a free account that asks for a
// pro-only model gets 11158 ("model [x] is not allowed for user type
// [personal_free_user]"). The plan itself is available from
// /v2/billing/meter/get-payment-type — the panel already fetches it for its
// Free/Pro badge — and the 11158 answer names the type outright, so routing can
// learn it from a failure too.
//
// This file remembers the tier per account so a pro-only model is never sent to
// an account known to be free. A trial pack is deliberately NOT treated as Pro:
// measured on 2026-10-18, an account holding the "Pro Plan Trial" pack still
// reported paymentType=free and still drew 11158 for gpt-6.1-sol.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	tierFree = "free"
	tierPro  = "pro"
	// modelNotAllowedCode is the upstream's plan gate: "model [x] is not allowed
	// for user type [personal_free_user]".
	modelNotAllowedCode = "11158"
	// tierFreeModelCooldown parks one free account on one pro-only model after
	// the refusal, so the next request skips it instead of paying another
	// round-trip to learn the same thing.
	tierFreeModelCooldown = 30 * time.Minute
)

// accountTierRec is the persisted plan state for one account.
type accountTierRec struct {
	Tier   string `json:"tier"`
	Source string `json:"source,omitempty"`
	Since  string `json:"since"`
}

// upstreamCodeRe extracts the business code from an upstream error envelope.
var upstreamCodeRe = regexp.MustCompile(`"code"\s*:\s*(\d+)`)

// upstreamErrorCode returns the business code in an upstream body, "" if absent.
func upstreamErrorCode(body string) string {
	if m := upstreamCodeRe.FindStringSubmatch(body); len(m) == 2 {
		return m[1]
	}
	return ""
}

// normalizeTier maps a plan/payment-type string to a known tier. Unknown values
// return "" on purpose: an unrecognised plan must not be treated as either
// free (which would block paid users) or pro (which would waste a round-trip).
func normalizeTier(plan string) string {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "free", "trial", "personal_free_user", "basic", "starter":
		return tierFree
	case "pro", "personal_pro_user", "plus", "premium", "subscription", "team", "enterprise":
		return tierPro
	}
	return ""
}

// markAccountTier records a tier under every identity the account is known by.
// Best effort on the state write; a failed write only costs a re-learn later.
func markAccountTier(keys []string, tier, source string) {
	keys = uniqueNonEmptyKeys(keys)
	if len(keys) == 0 || tier == "" {
		return
	}
	_ = mutateState(func(st *wbStateFile) error {
		if st.Tiers == nil {
			st.Tiers = map[string]accountTierRec{}
		}
		for _, k := range keys {
			if old, ok := st.Tiers[k]; ok && old.Tier == tier {
				continue // keep the first sighting: it dates the tier
			}
			st.Tiers[k] = accountTierRec{Tier: tier, Source: source, Since: time.Now().UTC().Format(time.RFC3339)}
		}
		return nil
	})
}

// accountTierFor returns the known tier for any of the account's keys.
func accountTierFor(keys ...string) (string, bool) {
	st, err := loadState()
	if err != nil || st.Tiers == nil {
		return "", false
	}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if rec, ok := st.Tiers[k]; ok && rec.Tier != "" {
			return rec.Tier, true
		}
	}
	return "", false
}

// rememberAccountPlan records what the billing endpoint reported, so routing
// knows the tier before a request has to fail first. Called from the panel's
// credit fetch, which already pays for this round-trip.
func rememberAccountPlan(keys []string, plan string) {
	if tier := normalizeTier(plan); tier != "" {
		markAccountTier(keys, tier, "payment-type")
	}
}

// candidateProOnlyBlocked reports whether this candidate must be skipped for the
// model it would send: a pro-only model on an account known to be free. Unknown
// tiers are not blocked — they get one attempt, and the 11158 answer teaches the
// tier for next time.
func candidateProOnlyBlocked(c *failoverCandidate) (bool, string) {
	if c == nil || strings.TrimSpace(c.RateModel) == "" {
		return false, ""
	}
	realm := realmCN
	if c.Auth != nil && isGlobalDomain(c.Auth.Auth.Domain) {
		realm = realmGlobal
	}
	if !isProOnlyModel(realm, c.RateModel) {
		return false, ""
	}
	tier, known := accountTierFor(candidateCooldownIDs(c)...)
	return known && tier == tierFree, c.RateModel
}

// markCandidateModelNotAllowed records that the upstream refused this model for
// this account. 11158 means the account is on the free plan, and the model is
// parked for it so the next request skips instead of re-learning.
func markCandidateModelNotAllowed(c *failoverCandidate) {
	if c == nil {
		return
	}
	keys := candidateCooldownIDs(c)
	markAccountTier(keys, tierFree, "upstream-11158")
	until := time.Now().Add(tierFreeModelCooldown)
	for _, key := range keys {
		markAuthCooldownReason(modelCooldownKey(key, c.RateModel), until, "model_not_allowed")
	}
	fmt.Fprintf(os.Stderr, "[workbuddy] tier: auth=%q model=%q marked free (11158) until=%s\n",
		redactSecrets(c.AuthID), redactSecrets(c.RateModel), until.UTC().Format(time.RFC3339))
}

// proTierBlockedMessage is the client-facing error when every eligible account
// is on the free plan: a plain statement beats the raw upstream envelope.
func proTierBlockedMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "this model requires a Pro account"
	}
	return fmt.Sprintf("model %s requires a Pro account (all eligible accounts are on the free plan)", model)
}
