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

// ---- Dynamic prefix emission + strip (Model Manager decision 4) ----
//
// The Model Manager prefix settings replace the hardcoded cn/ behaviour:
// effectiveSettings(realm).Prefix{On,Val} decides whether the realm's models
// are emitted with a prefix and which one (empty value + On = realm default:
// cn/ for CN, cb/ for Global). Global default is OFF (bare IDs, the historical
// behaviour), CN default is ON cn/.

// mmEffectivePrefix returns the prefix string to emit for a realm ("" = bare).
// On with an empty value = realm default (cn/ for CN, cb/ for Global).
func mmEffectivePrefix(realm string) string {
	p := effectiveSettings(realm, "").Prefix
	if !p.On {
		return ""
	}
	v := strings.TrimSpace(p.Val)
	if v == "" {
		return mmDefaultPrefix(realm)
	}
	if !strings.HasSuffix(v, "/") {
		v += "/"
	}
	return v
}

// mmDefaultPrefix is the per-realm default prefix (used when On && Val == "").
func mmDefaultPrefix(realm string) string {
	if realm == realmCN {
		return "cn/"
	}
	return "cb/"
}

// applyDynamicPrefix prefixes/normalizes the emitted model IDs for one realm.
// Replaces prefixCNModels (hardcoded cn/) in the Model Manager flow.
func applyDynamicPrefix(models []pluginapi.ModelInfo, realm string) []pluginapi.ModelInfo {
	pfx := mmEffectivePrefix(realm)
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if pfx == "" {
			// bare: strip a leading default prefix if the id carries one
			if strings.HasPrefix(strings.ToLower(m.ID), "cn/") {
				m.ID = strings.TrimSpace(m.ID[3:])
			}
		} else {
			if !strings.HasPrefix(strings.ToLower(m.ID), strings.ToLower(pfx)) {
				m.ID = pfx + m.ID
			}
		}
		out = append(out, m)
	}
	return out
}

// mmNudgeModelRegistry re-saves one auth file (identical content) through
// host.auth.save. The host treats a save as a config change and reloads its
// clients + model registry, so a prefix/disabled change is visible on
// /v1/models immediately — no CPA restart needed. Cosmetic no-op otherwise.
func mmNudgeModelRegistry(realm string) {
	for _, authID := range mmAccountsForRealmFn(realm) {
		sa, _, err := hostAuthGetBundle(authID)
		if err != nil || sa == nil {
			continue
		}
		raw, err := json.Marshal(sa)
		if err != nil {
			continue
		}
		_ = hostAuthSaveJSON(authID, raw)
		return // satu file cukup untuk memicu reload
	}
}

// mmFilterDisabled removes models the user turned off in the Model Manager
// (realm layer resolved for this auth). Strips the emitted prefix before the
// lookup because the disabled list stores bare catalog ids.
func mmFilterDisabled(models []pluginapi.ModelInfo, realm string) []pluginapi.ModelInfo {
	st := effectiveSettings(realm, "")
	if len(st.Disabled) == 0 {
		return models
	}
	off := make(map[string]struct{}, len(st.Disabled))
	for _, id := range st.Disabled {
		off[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		id := strings.ToLower(strings.TrimSpace(m.ID))
		// compare bare and prefixed forms so emission prefix never hides the rule
		if _, hit := off[id]; hit {
			continue
		}
		for pfx := range map[string]struct{}{"cb/": {}, "cn/": {}} {
			if strings.HasPrefix(id, pfx) {
				if _, hit2 := off[strings.TrimPrefix(id, pfx)]; hit2 {
					goto next
				}
			}
		}
		out = append(out, m)
	next:
	}
	return out
}

// mmRealmForAuth resolves the realm of the auth behind a storage JSON.
func mmRealmForAuth(storageJSON []byte) string {
	if sa, err := parseStored(storageJSON); err == nil {
		return accountRegion(sa)
	}
	if authUsesGlobalModels(storageJSON) {
		return realmGlobal
	}
	return realmCN
}

// stripDynamicPrefix removes whatever prefix the realm currently emits from a
// requested model id (client-facing alias → upstream id). Falls back to the
// hardcoded cn/ strip when the requested prefix matches the other realm's
// default (requesting cn/x from the Global realm still resolves x).
func stripDynamicPrefix(model string, realm string) (string, bool) {
	m := strings.TrimSpace(model)
	low := strings.ToLower(m)
	// current realm's own prefix
	if pfx := mmEffectivePrefix(realm); pfx != "" && strings.HasPrefix(low, strings.ToLower(pfx)) {
		return strings.TrimSpace(m[len(pfx):]), true
	}
	// the other realm's default prefix (cn/ or cb/) still strips — keeps old
	// client configs (cn/glm-5.3) working after a prefix change.
	for _, other := range []string{"cn/", "cb/"} {
		if !strings.EqualFold(other, mmEffectivePrefix(realm)) && strings.HasPrefix(low, other) {
			return strings.TrimSpace(m[len(other):]), true
		}
	}
	return m, false
}

// mmRealmHintFromAttributes guesses the realm for a request from its routing
// attributes (falls back to the requested id itself when unavailable).
func mmRealmHintFromAttributes(attributes map[string]string) string {
	for _, v := range attributes {
		lv := strings.ToLower(v)
		if strings.Contains(lv, "global") || strings.Contains(lv, "intl") || strings.Contains(lv, "cb/") {
			return realmGlobal
		}
		if strings.Contains(lv, "cn/") {
			return realmCN
		}
	}
	return realmCN
}
