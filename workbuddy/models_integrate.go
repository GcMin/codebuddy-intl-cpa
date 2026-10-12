// models_integrate.go wires the Model Manager endpoints that need cross-module
// access (W1d): the upstream refresh fan-out, the per-model live test and the
// legacy models stub removal glue. It exists as a separate file so
// models_config.go (W1a) and update_check.go (W1c) stay untouched; everything
// here is glue over the pieces W1a/W1c delivered.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// mmChatHTTPDo is the HTTP seam for the model test request (tests swap it).
var mmChatHTTPDo = hostHTTPDo

// handleModelsRefresh serves POST /models/refresh:
//
//	{realm: "global"|"cn"} — fan-out: drop the cached upstream catalog of every
//	account in the realm, then re-fetch (cheap console GETs, no chat credits).
//	Returns the refreshed payload for the realm so the panel can render
//	immediately without a second round-trip.
func handleModelsRefresh(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Realm string `json:"realm"`
	}
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return map[string]any{"error": "invalid body: " + err.Error()}
		}
	}
	realm := strings.ToLower(strings.TrimSpace(body.Realm))
	if realm != realmGlobal && realm != realmCN {
		return map[string]any{"error": "realm must be global or cn"}
	}
	accounts := mmAccountsForRealmFn(realm)
	if len(accounts) == 0 {
		return map[string]any{"error": "no " + realm + " accounts found"}
	}
	refreshed, failures := 0, 0
	var lastErr string
	for _, authID := range accounts {
		invalidateUpstreamModels(authID)
		if _, _, err := upstreamModelsCached(authID); err != nil {
			failures++
			lastErr = err.Error()
			continue
		}
		refreshed++
	}
	if failures > 0 && refreshed == 0 {
		return map[string]any{"error": "refresh failed for all accounts: " + lastErr}
	}
	out := modelsConfigPayload(map[string]string{realm: accounts[0]})
	out["refresh"] = map[string]any{"refreshed": refreshed, "failed": failures, "total": len(accounts)}
	if lastErr != "" {
		out["refresh"].(map[string]any)["last_error"] = lastErr
	}
	return out
}

// handleModelsTest serves POST /models/test:
//
//	{realm, auth, model} — one tiny real chat completion against the upstream
//	using the selected account. Returns status, served model, token usage and
//	credit so the panel's Test column shows ground truth, not catalog claims.
//	Cost guard: max_tokens 32 and a trivial prompt — verified credit cost of
//	this shape is 0.00–0.04 on the cheapest models, bounded by max_tokens.
func handleModelsTest(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Realm   string `json:"realm"`
		Auth    string `json:"auth"`
		Account string `json:"account"` // alias used by the panel (W2)
		Model   string `json:"model"`
	}
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return map[string]any{"error": "invalid body: " + err.Error()}
		}
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		return map[string]any{"error": "model is required"}
	}
	authID := strings.TrimSpace(body.Auth)
	if authID == "" {
		authID = strings.TrimSpace(body.Account)
	}
	if authID == "" {
		return map[string]any{"error": "auth is required"}
	}
	target, err := resolveFetchTarget(authID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}

	payload := map[string]any{
		"model":      model,
		"stream":     true, // upstream rejects non-stream (code 11101)
		"messages":   []map[string]any{{"role": "system", "content": "You are a helpful assistant."}, {"role": "user", "content": "reply with exactly: OK"}},
		"max_tokens": 32,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}

	base := mmUpstreamBaseGlobal
	if target.Realm == realmCN {
		base = mmUpstreamBaseCN
	}
	ctxDone := make(chan struct{})
	defer close(ctxDone)
	httpReq, err := http.NewRequest(http.MethodPost, base+"/v2/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	httpReq.Header.Set("Authorization", "Bearer "+target.Token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Origin", target.Origin)
	httpReq.Header.Set("Referer", target.Origin+"/")
	httpReq.Header.Set("User-Agent", workBuddyUA)
	if target.UID != "" {
		httpReq.Header.Set("X-User-Id", target.UID)
	}
	resp, err := mmChatHTTPDo(httpReq)
	if err != nil {
		return map[string]any{"ok": false, "status": 0, "error": "upstream request failed: " + err.Error()}
	}

	out := map[string]any{"ok": resp.StatusCode == http.StatusOK, "status": resp.StatusCode, "http_status": resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		// Upstream error envelope: {"code":11102,"msg":"..."} — surface code+msg
		// so the panel can explain retired models (11102) vs effort (11150).
		var apiErr struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(resp.Body, &apiErr); err == nil && apiErr.Code != 0 {
			out["upstream_code"] = apiErr.Code
			out["error"] = fmt.Sprintf("upstream %d: code %d %s", resp.StatusCode, apiErr.Code, strings.TrimSpace(apiErr.Msg))
		} else {
			out["error"] = fmt.Sprintf("upstream status %d", resp.StatusCode)
		}
		return out
	}

	// Fold the SSE stream manually: last usage chunk carries credit + served model.
	var content, served string
	var usage map[string]any
	for _, line := range strings.Split(string(resp.Body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil {
			continue
		}
		if chunk.Model != "" {
			served = chunk.Model
		}
		for _, c := range chunk.Choices {
			content += c.Delta.Content
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	out["served"] = served
	out["content"] = truncateTestContent(content, 40)
	if usage != nil {
		if v, ok := usage["total_tokens"].(float64); ok {
			out["total_tokens"] = int64(v)
		}
		if v, ok := usage["credit"].(float64); ok {
			out["credit"] = v
		}
	}
	if served != "" && served != model {
		out["redirected"] = true
	}
	return out
}

func truncateTestContent(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
