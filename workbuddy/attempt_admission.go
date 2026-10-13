// attempt_admission.go bounds chat concurrency without replaying accepted
// requests. Limits are process-local; separate CPA instances do not share slots.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const maxAccountChatInFlight = 1
const maxAccountChatWaiters = 16
const accountChatQueueTimeout = 30 * time.Second

type accountChatGate struct {
	slots   chan struct{}
	waiters int
	refs    int // waiting + admitted; idle accounts leave no retained gate
}

var chatGateMu sync.Mutex
var chatGates = map[string]*accountChatGate{}

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
	now := time.Now()
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

// Returns (nil,nil) for quarantined candidates, (nil,error) for local overload,
// and a release callback only after a slot and any probe have been admitted.
// Cooldown is rechecked AFTER queueing so stale pools cannot fan out a burst.
func admitCandidateAttempt(ctx context.Context, c *failoverCandidate) (func(), error) {
	if c == nil || c.Auth == nil || c.AuthID == "" {
		return nil, nil
	}
	cd := candidateCooldownState(c)
	if !cd.NextRetryAt.IsZero() && (!c.RecoveryProbe || cd.Reason != "429" || !candidateProbeReady(c)) {
		return nil, nil
	}
	key := accountGateKey(c)
	chatGateMu.Lock()
	gate := chatGates[key]
	if gate == nil {
		gate = &accountChatGate{slots: make(chan struct{}, maxAccountChatInFlight)}
		chatGates[key] = gate
	}
	if gate.waiters >= maxAccountChatWaiters {
		chatGateMu.Unlock()
		return nil, &upstreamStatusError{status: 429, body: "local account chat queue is full; retry later"}
	}
	gate.waiters++
	gate.refs++
	chatGateMu.Unlock()
	waiting := true
	dropRef := func() {
		chatGateMu.Lock()
		gate.refs--
		if gate.refs == 0 && gate.waiters == 0 && chatGates[key] == gate {
			delete(chatGates, key)
		}
		chatGateMu.Unlock()
	}
	defer func() {
		if waiting {
			chatGateMu.Lock()
			gate.waiters--
			chatGateMu.Unlock()
			dropRef()
		}
	}()
	timer := time.NewTimer(accountChatQueueTimeout)
	defer timer.Stop()
	select {
	case gate.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, &upstreamStatusError{status: 429, body: "local account chat queue timed out; retry later"}
	}
	chatGateMu.Lock()
	gate.waiters--
	chatGateMu.Unlock()
	waiting = false
	var once sync.Once
	release := func() { once.Do(func() { <-gate.slots; dropRef() }) }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	cd = candidateCooldownState(c)
	if !cd.NextRetryAt.IsZero() {
		if !c.RecoveryProbe || cd.Reason != "429" || !candidateProbeReady(c) {
			release()
			return nil, nil
		}
		// Slot serializes every alias/model for this account. Reserving the next
		// probe before I/O prevents concurrent callers from probing in succession.
		cooldownMu.Lock()
		now := time.Now()
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
	}
	return release, nil
}
