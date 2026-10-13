package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func readyProbe(t *testing.T, c *failoverCandidate) {
	t.Helper()
	cooldownMu.Lock()
	for _, id := range candidateCooldownIDs(c) {
		for _, key := range []string{id, modelCooldownKey(id, c.RateModel)} {
			if cd, ok := cooldowns[key]; ok {
				cd.ProbeAt = time.Now().Add(-time.Second)
				cooldowns[key] = cd
			}
		}
	}
	cooldownMu.Unlock()
}

func TestRecoveryProbeExecutorPaths(t *testing.T) {
	for _, model := range []string{"cb/gpt-6.1-sol", "gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			for _, mode := range []string{"nonstream", "syncstream", "asyncstream"} {
				for _, rejection := range []bool{false, true} {
					t.Run(mode+map[bool]string{false: "/recovered", true: "/still-limited"}[rejection], func(t *testing.T) {
						t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
						cands := exhaustedCandidates(t)
						for _, c := range cands {
							c.Auth.Auth.AccessToken = "fake-test-token"
							c.RateModel = "gpt-6.1-sol"
							markCandidateUnavailable(c, outcomeRateLimited, "")
							readyProbe(t, c)
						}
						files := []pluginapi.HostAuthFileEntry{testAuthEntry("exhaust-a", "record-a", false), testAuthEntry("exhaust-b", "record-b", false)}
						stubAuthSources(t, files, map[string]*storedAuth{"exhaust-a": cands[0].Auth, "exhaust-b": cands[1].Auth})
						oldStore := usageStore
						usageStore = &requestUsageStore{perDay: map[string]map[string]*dayAgg{}}
						t.Cleanup(func() { usageStore = oldStore })
						oldEmit := streamEmitSource
						streamEmitSource = func(string, []byte) error { return nil }
						t.Cleanup(func() { streamEmitSource = oldEmit })
						client := sharedHTTPClient()
						oldTransport := client.Transport
						t.Cleanup(func() { client.Transport = oldTransport })
						calls := 0
						client.Transport = exhaustedMockTransport(func(r *http.Request) (*http.Response, error) {
							if !strings.HasSuffix(r.URL.Path, "/v2/chat/completions") {
								t.Fatalf("unexpected path %s", r.URL.Path)
							}
							calls++
							requestBody, _ := io.ReadAll(r.Body)
							if r.Header.Get("X-Conversation-ID") != stableConversationID(requestBody) {
								t.Fatal("probe changed CID")
							}
							var body map[string]any
							if json.Unmarshal(requestBody, &body) != nil || body["model"] != "gpt-6.1-sol" {
								t.Fatal("probe changed model/payload")
							}
							if r.Header.Get("X-User-Id") != "uid-a" {
								t.Fatal("routed account preference lost")
							}
							status, payload := 200, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":1,\"credit\":0.01}}\n\ndata: [DONE]\n\n"
							if rejection {
								status, payload = 429, `{"code":6004,"msg":"frequency limit"}`
							}
							return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}, nil
						})
						req := executorStreamRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: model, AuthID: "exhaust-a", StorageJSON: storageJSONForAuth(cands[0].Auth), Payload: []byte(`{"model":"cb/gpt-6.1-sol","messages":[{"role":"system","content":"test"},{"role":"user","content":"hello"}]}`)}}
						raw, _ := json.Marshal(req)
						var err error
						switch mode {
						case "nonstream":
							_, err = handleExecExecute(raw)
						case "syncstream":
							_, err = handleExecStream(raw)
						default:
							pool, storage, e := buildRequestFailoverPool(req.Model, req.AuthID, req.StorageJSON)
							if e != nil {
								t.Fatal(e)
							}
							pumpWithFailover(t.Context(), nil, req, prepareUpstreamBody(req.Payload, nil, cands[0].Auth, "gpt-6.1-sol"), "gpt-6.1-sol", "uid-a", time.Now(), false, pool.ordered(storage))
						}
						if calls != 1 {
							t.Fatalf("expected exactly one real attempt, got %d err=%v", calls, err)
						}
						if !rejection && err != nil {
							t.Fatal(err)
						}
						records := usageStore.ringSnapshot()
						if len(records) != 1 || records[0].Failed != rejection || records[0].CacheTrace == nil || len(records[0].CacheTrace.Attempts) != 1 {
							t.Fatalf("bad usage %+v", records)
						}
						if rejection && candidateProbeReady(cands[0]) {
							t.Fatal("rejected probe did not renew backoff")
						}
					})
				}
			}
		})
	}
}

func TestModelRateLimitIsolationAndCreditScope(t *testing.T) {
	cands := exhaustedCandidates(t)
	a := cands[0]
	a.RateModel = "deepseek-v4.1-flash"
	t.Cleanup(func() { a.RateModel = ""; resetCandidateCooldown(a) })
	markCandidateUnavailable(a, outcomeRateLimited, "")
	b := *a
	b.RateModel = "gpt-6.1-sol"
	resetCandidateCooldown(&b)
	if !candidateOnCooldown(a) {
		t.Fatal("success on GPT cleared DeepSeek rate cooldown")
	}
	if candidateOnCooldown(&b) {
		t.Fatal("DeepSeek soft429 poisoned GPT")
	}
	if !candidateOnCooldown(a) {
		t.Fatal("same model lost cooldown")
	}
	markCandidateUnavailable(a, outcomeAccountExhausted, "")
	if !candidateOnCooldown(&b) || candidateSoftCooldown(&b) {
		t.Fatal("credit rejection must cover all models and never probe")
	}
	// A late soft limit must not overwrite the stronger account rejection.
	markAuthCooldownReason(a.AuthID, time.Now().Add(time.Minute), "429")
	if candidateCooldownState(a).Reason != "credits_exhausted" {
		t.Fatal("soft error overwrote credit rejection")
	}
}

func TestProbeRankingAliasesDisabledRealmAndCredit(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
	cs := exhaustedCandidates(t)
	files := []pluginapi.HostAuthFileEntry{testAuthEntry("exhaust-a", "record-a", false), testAuthEntry("exhaust-b", "record-b", false), testAuthEntry("cn", "cn", false), testAuthEntry("disabled", "disabled", true)}
	creds := map[string]*storedAuth{"exhaust-a": cs[0].Auth, "exhaust-b": cs[1].Auth, "cn": {Auth: storedTokens{Domain: "https://copilot.tencent.com"}}, "disabled": cs[0].Auth}
	stubAuthSources(t, files, creds)
	for _, c := range cs {
		markCandidateUnavailable(c, outcomeRateLimited, "")
		readyProbe(t, c)
	}
	pool, _, err := buildRealmFailoverPool("cb/gpt-6.1-sol", "exhaust-a", nil, realmGlobal)
	if err != nil || pool.Primary.AuthID != "exhaust-a" || !pool.Primary.RecoveryProbe {
		t.Fatalf("lost routed preference %+v %v", pool, err)
	}
	// Earliest effective alias reset wins when neither probe is ready.
	cooldownMu.Lock()
	cooldowns["exhaust-a"] = authCooldown{Reason: "429", NextRetryAt: time.Now().Add(time.Minute), ProbeAt: time.Now().Add(10 * time.Second)}
	cooldowns["uid-a"] = authCooldown{Reason: "429", NextRetryAt: time.Now().Add(9 * time.Minute), ProbeAt: time.Now().Add(90 * time.Second)}
	cooldowns["exhaust-b"] = authCooldown{Reason: "429", NextRetryAt: time.Now().Add(2 * time.Minute), ProbeAt: time.Now().Add(20 * time.Second)}
	cooldownMu.Unlock()
	pool, _, err = buildRealmFailoverPool("cb/gpt-6.1-sol", "missing", nil, realmGlobal)
	if err != nil || pool.Primary.AuthID != "exhaust-b" {
		t.Fatalf("alias expiry ignored %+v %v", pool, err)
	}
	for _, c := range cs {
		markCandidateUnavailable(c, outcomeAccountExhausted, "")
	}
	pool, storage, err := buildRealmFailoverPool("cb/gpt-6.1-sol", "exhaust-a", nil, realmGlobal)
	if err != nil {
		t.Fatal(err)
	}
	fin := runFailover(pool.ordered(storage), func(*failoverCandidate) (attemptOutcome, int, string, error) {
		t.Fatal("exhausted/CN/disabled must never execute")
		return outcomeSuccess, 200, "", nil
	})
	if len(fin.Tried) != 0 || fin.Err == nil {
		t.Fatalf("bad terminal result %+v", fin)
	}
}

func TestBurstSerializesAliasesAndRechecksCooldown(t *testing.T) {
	cs := exhaustedCandidates(t)
	a := cs[0]
	alias := *a
	alias.AuthID = "other-runtime-id" // same UID, different host identity
	var inFlight, peak, calls atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			c := a
			if i%2 == 1 {
				c = &alias
			}
			runFailover([]*failoverCandidate{c}, func(*failoverCandidate) (attemptOutcome, int, string, error) {
				n := inFlight.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				inFlight.Add(-1)
				return outcomeRateLimited, 429, "frequency limit", errors.New("429")
			})
		}(i)
	}
	close(start)
	wg.Wait()
	if peak.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("burst bypassed admission or stale cooldown: peak=%d calls=%d", peak.Load(), calls.Load())
	}
	resetCandidateCooldown(&alias)
}

func TestConcurrentProbeSingleFlightBackoffAndQueueCancellation(t *testing.T) {
	c := exhaustedCandidates(t)[0]
	markCandidateUnavailable(c, outcomeRateLimited, "")
	readyProbe(t, c)
	c.RecoveryProbe = true
	var calls atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			runFailover([]*failoverCandidate{c}, func(*failoverCandidate) (attemptOutcome, int, string, error) {
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				return outcomeRateLimited, 429, "frequency limit", errors.New("429")
			})
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("probe storm: %d", calls.Load())
	}
	resetCandidateCooldown(c)
	c.RecoveryProbe = false
	release, err := admitCandidateAttempt(context.Background(), c)
	if err != nil || release == nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, e := admitCandidateAttempt(ctx, c); r != nil || !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel ignored: acquired=%v err=%v", r != nil, e)
	}
	// Full queue rejects immediately, not a destructive auth cooldown.
	chatGateMu.Lock()
	g := chatGates[accountGateKey(c)]
	g.waiters = maxAccountChatWaiters
	chatGateMu.Unlock()
	r, e := admitCandidateAttempt(context.Background(), c)
	chatGateMu.Lock()
	g.waiters = 0
	chatGateMu.Unlock()
	if r != nil || e == nil || candidateOnCooldown(c) {
		t.Fatalf("bad overload admission acquired=%v err=%v", r != nil, e)
	}
	release()
	release() // idempotent, no deadlock
	chatGateMu.Lock()
	_, retained := chatGates[accountGateKey(c)]
	chatGateMu.Unlock()
	if retained {
		t.Fatal("idle gate leaked")
	}
}
