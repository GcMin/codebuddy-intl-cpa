// attempt_admission.go bounds chat concurrency per account without a local
// waiting queue. A busy candidate is skipped so another enabled account in
// the same realm can answer immediately. The host may still enforce its own
// request scheduling before the plugin executor is called.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const maxAccountChatInFlight = 2

var errAccountBusy = errors.New("account chat capacity reached")

type accountChatGate struct{ inFlight int }

var chatGateMu sync.Mutex
var chatGates = map[string]*accountChatGate{}

// This is the same state used for admission, not a separately estimated load.
// The map is a snapshot so a management response never holds the gate lock
// while encoding or serving HTTP. Idle accounts are absent and count as zero.
func accountInFlightSnapshot() map[string]int {
	chatGateMu.Lock()
	defer chatGateMu.Unlock()
	out := make(map[string]int, len(chatGates))
	for key, gate := range chatGates {
		out[key] = gate.inFlight
	}
	return out
}

func candidateRateModel(model string, auth *storedAuth) string {
	if strings.TrimSpace(model) == "" {
		return ""
	}
	realm := ""
	if auth != nil {
		realm = accountRegion(auth)
	}
	return resolveUpstreamModel(model, realm, nil)
}
func modelCooldownKey(id, model string) string { return "model:" + id + ":" + model }

// Use the maximum expiry across every identity, and preserve hard-credit
// rejection even if another alias/model has a newer soft limit.
func candidateCooldownState(c *failoverCandidate) authCooldown {
	cooldownMu.Lock()
	defer cooldownMu.Unlock()
	return candidateCooldownStateLocked(c, time.Now())
}

func candidateCooldownStateLocked(c *failoverCandidate, now time.Time) authCooldown {
	var out authCooldown
	for _, id := range candidateCooldownIDs(c) {
		keys := []string{id}
		if c.RateModel != "" {
			keys = append(keys, modelCooldownKey(id, c.RateModel))
		}
		for _, key := range keys {
			cd, ok := cooldowns[key]
			if !ok {
				continue
			}
			if !cd.NextRetryAt.After(now) {
				delete(cooldowns, key)
				continue
			}
			if cd.NextRetryAt.After(out.NextRetryAt) {
				out.NextRetryAt = cd.NextRetryAt
			}
			if cd.ProbeAt.After(out.ProbeAt) {
				out.ProbeAt = cd.ProbeAt
			}
			if cd.Reason == "credits_exhausted" || out.Reason == "" {
				out.Reason = cd.Reason
			}
		}
	}
	return out
}
func candidateSoftCooldown(c *failoverCandidate) bool {
	cd := candidateCooldownState(c)
	return !cd.NextRetryAt.IsZero() && cd.Reason == "429"
}
func candidateProbeAt(c *failoverCandidate) time.Time {
	cd := candidateCooldownState(c)
	if cd.ProbeAt.IsZero() {
		return cd.NextRetryAt
	}
	return cd.ProbeAt
}
func candidateProbeReady(c *failoverCandidate) bool { return !candidateProbeAt(c).After(time.Now()) }

func attemptContext(contexts []context.Context) context.Context {
	if len(contexts) > 0 && contexts[0] != nil {
		return contexts[0]
	}
	return context.Background()
}
func accountGateKey(c *failoverCandidate) string {
	if c.Auth != nil && c.Auth.Account.UID != "" {
		return accountRegion(c.Auth) + ":" + c.Auth.Account.UID
	}
	return c.AuthID
}

// Acquires only a free slot. The caller can immediately try another account
// when errAccountBusy is returned. On recheck, a newly quarantined account is
// skipped without an upstream call. Release is idempotent and removes idle
// gates. The slot serializes probe reservation with candidate admission.
func admitCandidateAttempt(ctx context.Context, c *failoverCandidate) (func(), error) {
	if c == nil || c.Auth == nil || c.AuthID == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cd := candidateCooldownState(c)
	if !cd.NextRetryAt.IsZero() && (!c.RecoveryProbe || cd.Reason != "429" || !candidateProbeReady(c)) {
		return nil, nil
	}
	key := accountGateKey(c)
	chatGateMu.Lock()
	gate := chatGates[key]
	if gate == nil {
		gate = &accountChatGate{}
		chatGates[key] = gate
	}
	if gate.inFlight >= maxAccountChatInFlight {
		chatGateMu.Unlock()
		return nil, errAccountBusy
	}
	gate.inFlight++
	chatGateMu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			chatGateMu.Lock()
			gate.inFlight--
			if gate.inFlight == 0 && chatGates[key] == gate {
				delete(chatGates, key)
			}
			chatGateMu.Unlock()
		})
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	// Recheck and reserve under the SAME lock. Two in-flight slots must not
	// both pass a ready-probe check before either reserves the next backoff.
	cooldownMu.Lock()
	now := time.Now()
	cd = candidateCooldownStateLocked(c, now)
	if !cd.NextRetryAt.IsZero() {
		if !c.RecoveryProbe || cd.Reason != "429" || cd.ProbeAt.After(now) {
			cooldownMu.Unlock()
			release()
			return nil, nil
		}
		// Reserve the next probe before I/O. A second slot sees this
		// reservation and skips the attempt.
		for _, id := range candidateCooldownIDs(c) {
			keys := []string{id}
			if c.RateModel != "" {
				keys = append(keys, modelCooldownKey(id, c.RateModel))
			}
			for _, k := range keys {
				v, ok := cooldowns[k]
				if ok && v.Reason == "429" && v.NextRetryAt.After(now) {
					v.ProbeAt = now.Add(rateLimitProbeInterval)
					cooldowns[k] = v
				}
			}
		}
		cooldownMu.Unlock()
		fmt.Fprintf(os.Stderr, "[workbuddy] failover: probing rate-limited auth=%q model=%q\n", redactSecrets(c.AuthID), redactSecrets(c.RateModel))
		return release, nil
	}
	cooldownMu.Unlock()
	return release, nil
}
