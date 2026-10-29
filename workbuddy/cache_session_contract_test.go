package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestExplicitConversationIdentityStableScopedAndLegacyCompatible(t *testing.T) {
	stubClientVersionRefresh(t)
	a := []byte(`{"messages":[{"role":"system","content":"rules"},{"role":"user","content":"state A"}]}`)
	b := []byte(`{"messages":[{"role":"system","content":"review rules"},{"role":"user","content":"state B"}]}`)
	req := pluginapi.ExecutorRequest{Model: "cn/deepseek-v4-flash", Headers: http.Header{"Authorization": {"Bearer fixture-client"}, "X-Session-Id": {"same-game"}}}
	cid := explicitConversationID(req, a, realmCN)
	if len(cid) != 32 || cid != explicitConversationID(req, b, realmCN) || strings.Contains(cid, "same-game") {
		t.Fatal("unstable or raw identity")
	}
	for _, variant := range []string{"client", "model", "realm", "session"} {
		other := req
		other.Headers = req.Headers.Clone()
		realm := realmCN
		switch variant {
		case "client":
			other.Headers.Set("Authorization", "Bearer other-client")
		case "model":
			other.Model = "cn/other-model"
		case "realm":
			realm = realmGlobal
		case "session":
			other.Headers.Set("X-Session-ID", "other-game")
		}
		if explicitConversationID(other, a, realm) == cid {
			t.Fatalf("missing %s isolation", variant)
		}
	}
	for _, body := range [][]byte{a, b} {
		out, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid", bytes.NewReader(body))
		backendHeaders(out, &storedAuth{}, cid)
		if out.Header.Get("X-Conversation-ID") != cid {
			t.Fatal("identity not forwarded")
		}
		got, _ := io.ReadAll(out.Body)
		if !bytes.Equal(got, body) {
			t.Fatal("identity changed prompt")
		}
	}
	legacy := req
	legacy.Headers = req.Headers.Clone()
	legacy.Headers.Del("X-Session-ID")
	if explicitConversationID(legacy, a, realmCN) != "" {
		t.Fatal("legacy caller opted in")
	}
	out, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid", bytes.NewReader(a))
	backendHeaders(out, &storedAuth{})
	if out.Header.Get("X-Conversation-ID") != stableConversationID(a) {
		t.Fatal("legacy CID changed")
	}
	legacy.Headers.Set("Session-Id", "same-game")
	if explicitConversationID(legacy, b, realmCN) != cid {
		t.Fatal("legacy session header alias lost")
	}
	legacy.Headers.Del("Authorization")
	if explicitConversationID(legacy, a, realmCN) != "" {
		t.Fatal("unscoped client identity accepted")
	}
}

// Optional fixture is emitted by the real ds-star Provider + MockTransport.
// Normal CI uses equivalent fake wire requests without a cross-repo dependency.
type cacheWireRequest struct {
	Model   string          `json:"model"`
	Headers http.Header     `json:"headers"`
	Body    json.RawMessage `json:"body"`
}

func TestExecutorExplicitSessionContinuity(t *testing.T) {
	stubClientVersionRefresh(t)
	wire := []cacheWireRequest{}
	if path := os.Getenv("DS_STAR_CPA_WIRE_FIXTURE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
	} else {
		for _, text := range []string{"state A", "state B"} {
			body, _ := json.Marshal(map[string]any{"model": "cn/deepseek-v4-flash", "messages": []map[string]string{{"role": "system", "content": "rules"}, {"role": "user", "content": text}}})
			wire = append(wire, cacheWireRequest{"cn/deepseek-v4-flash", http.Header{"Authorization": {"Bearer fixture-client"}, "X-Session-Id": {"ds-star-v1-fixture"}}, body})
		}
	}
	if len(wire) < 2 {
		t.Fatal("need changing state requests")
	}
	for _, mode := range []string{"nonstream", "syncstream", "asyncstream"} {
		t.Run(mode, func(t *testing.T) {
			resetChatSessions(t)
			resetUsageMetaCache(t)
			cands := exhaustedCandidates(t)
			for _, c := range cands {
				c.Auth.Auth.Domain = "copilot.tencent.com"
				c.Auth.Auth.AccessToken = "fixture-token"
			}
			stubAuthSources(t, []pluginapi.HostAuthFileEntry{testAuthEntry("exhaust-a", "record-a", false), testAuthEntry("exhaust-b", "record-b", false)}, map[string]*storedAuth{"exhaust-a": cands[0].Auth, "exhaust-b": cands[1].Auth})
			oldStore := usageStore
			usageStore = &requestUsageStore{perDay: map[string]map[string]*dayAgg{}}
			t.Cleanup(func() { usageStore = oldStore })
			oldEmit := streamEmitSource
			streamEmitSource = func(string, []byte) error { return nil }
			t.Cleanup(func() { streamEmitSource = oldEmit })
			var cids, bodies []string
			restore := setDirectTransport(exhaustedMockTransport(func(r *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(r.URL.Path, "/v2/chat/completions") {
					t.Fatalf("unexpected network path %s", r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(raw))
				cids = append(cids, r.Header.Get("X-Conversation-ID"))
				payload := "data: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":1,\"total_tokens\":101,\"credit\":0.01}}\n\ndata: [DONE]\n\n"
				if len(cids) > 1 {
					payload = strings.Replace(payload, `"credit":0.01`, `"credit":0.01,"prompt_cache_write_tokens":0`, 1)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}, nil
			}))
			t.Cleanup(restore)
			for _, w := range wire {
				req := executorStreamRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: w.Model, Headers: w.Headers, AuthID: "exhaust-a", StorageJSON: storageJSONForAuth(cands[0].Auth), Payload: w.Body}}
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
					body := prepareUpstreamBody(req.Payload, nil, cands[0].Auth, "deepseek-v4-flash")
					pumpWithFailover(t.Context(), nil, req, body, "deepseek-v4-flash", "uid-a", time.Now(), false, pool.ordered(storage))
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(cids) != len(wire) || cids[0] == "" || bodies[0] == bodies[1] {
				t.Fatalf("wrong request shape/count: %d", len(cids))
			}
			for _, cid := range cids {
				if cid != cids[0] {
					t.Fatal("changed observation rotated upstream CID")
				}
			}
			rows := usageStore.queryRecords("", "", "", 100)
			counts := map[string]int{}
			for _, row := range rows {
				counts[row.CacheWriteStatus]++
				if row.CacheWriteTokens != 0 {
					t.Fatal("unexpected fixture write value")
				}
			}
			if len(rows) != len(wire) || counts["missing"] != 1 || counts["reported"] != len(wire)-1 {
				t.Fatalf("stream/fold lost write-field presence: %+v", counts)
			}
		})
	}
}
