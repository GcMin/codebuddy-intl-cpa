package main

import (
	"strings"
	"testing"
)

func TestNormalizeTier(t *testing.T) {
	cases := map[string]string{
		"free":                tierFree,
		"FREE":                tierFree,
		" trial ":             tierFree,
		"personal_free_user":  tierFree,
		"basic":               tierFree,
		"pro":                 tierPro,
		"personal_pro_user":   tierPro,
		"premium":             tierPro,
		"subscription":        tierPro,
		"enterprise":          tierPro,
		"":                    "",
		"something-unhandled": "",
	}
	for in, want := range cases {
		if got := normalizeTier(in); got != want {
			t.Errorf("normalizeTier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpstreamErrorCode(t *testing.T) {
	cases := map[string]string{
		`{"code":11158,"msg":"x"}`:     "11158",
		`{"code": 11140, "msg":"x"}`:   "11140",
		`{"error":{"code":6004}}`:      "6004",
		`{"msg":"no code here"}`:       "",
		`not json`:                     "",
		`{"code":11158}`:               "11158",
		`prefix {"code":14018} suffix`: "14018",
	}
	for in, want := range cases {
		if got := upstreamErrorCode(in); got != want {
			t.Errorf("upstreamErrorCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccountTierPersistsAndLookupByAnyKey(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
	keys := []string{"idx-1", "file-1", "uid-1"}
	markAccountTier(keys, tierFree, "test")

	for _, key := range keys {
		tier, ok := accountTierFor(key)
		if !ok || tier != tierFree {
			t.Errorf("accountTierFor(%q) = %q/%v, want free/true", key, tier, ok)
		}
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(st.Tiers) != len(keys) {
		t.Fatalf("persisted tiers = %d, want %d", len(st.Tiers), len(keys))
	}
	// An unknown account stays unknown — unknown must never block a request.
	if _, ok := accountTierFor("nobody"); ok {
		t.Error("unknown account reported a tier")
	}
}

func TestRememberAccountPlan(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
	rememberAccountPlan([]string{"plan-1"}, "free")
	if tier, ok := accountTierFor("plan-1"); !ok || tier != tierFree {
		t.Errorf("tier = %q/%v, want free/true", tier, ok)
	}
	rememberAccountPlan([]string{"plan-2"}, "not-a-plan")
	if _, ok := accountTierFor("plan-2"); ok {
		t.Error("an unhandled plan string must not be recorded as a tier")
	}
}

func TestCandidateProOnlyBlocked(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())

	global := &storedAuth{}
	global.Auth.Domain = "https://www.codebuddy.ai"
	cn := &storedAuth{}
	cn.Auth.Domain = "https://www.codebuddy.cn"

	newCand := func(sa *storedAuth, model string) *failoverCandidate {
		return &failoverCandidate{AuthID: "cand-" + model, Auth: sa, RateModel: model}
	}

	freeGlobal := newCand(global, "gpt-6.1-sol")
	rememberAccountPlan([]string{freeGlobal.AuthID}, "free")
	if blocked, model := candidateProOnlyBlocked(freeGlobal); !blocked || model != "gpt-6.1-sol" {
		t.Errorf("free + pro-only model: blocked=%v model=%q, want true/gpt-6.1-sol", blocked, model)
	}
	if blocked, _ := candidateProOnlyBlocked(newCand(global, "glm-5.3")); blocked {
		t.Error("free + free model must not be blocked")
	}
	if blocked, _ := candidateProOnlyBlocked(newCand(cn, "gpt-6.1-sol")); blocked {
		t.Error("the CN catalog has no measured pro-only model; must not block")
	}

	proGlobal := newCand(global, "gpt-6.1-sol")
	rememberAccountPlan([]string{proGlobal.AuthID}, "pro")
	if blocked, _ := candidateProOnlyBlocked(proGlobal); blocked {
		t.Error("pro account must not be blocked from a pro-only model")
	}

	unknown := newCand(global, "gpt-6.1-sol")
	if blocked, _ := candidateProOnlyBlocked(unknown); blocked {
		t.Error("unknown tier must be attempted once, not blocked")
	}
	if blocked, _ := candidateProOnlyBlocked(nil); blocked {
		t.Error("nil candidate must not be blocked")
	}
}

func TestMarkCandidateModelNotAllowedLearnsFreeTier(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
	cand := &failoverCandidate{AuthID: "learn-idx", RecordID: "learn-file", RateModel: "gpt-6.1-sol"}
	t.Cleanup(func() {
		resetAuthCooldown("learn-idx")
		resetAuthCooldown("learn-file")
		resetAuthCooldown(modelCooldownKey("learn-idx", "gpt-6.1-sol"))
	})

	markCandidateModelNotAllowed(cand)
	if tier, ok := accountTierFor("learn-idx"); !ok || tier != tierFree {
		t.Errorf("tier = %q/%v, want free/true (11158 means a free account)", tier, ok)
	}
	if _, ok := accountTierFor("learn-file"); !ok {
		t.Error("tier must be recorded under every candidate identity")
	}
	// The model is parked for this account so the next request skips it.
	if !isAuthOnCooldown(modelCooldownKey("learn-idx", "gpt-6.1-sol")) {
		t.Error("the refused model must be parked for this account")
	}
	// ...but the account itself is not parked for other models.
	if isAuthOnCooldown("learn-idx") {
		t.Error("a plan refusal must not park the whole account")
	}
}

func TestClassifyAccountFailurePlanGate(t *testing.T) {
	body := `{"code":11158,"msg":"model [gpt-6.1-sol] is not allowed for user type [personal_free_user]"}`
	if got := classifyAccountFailure(400, body); got != outcomeModelNotAllowed {
		t.Errorf("classifyAccountFailure(11158) = %v, want outcomeModelNotAllowed", got)
	}
	// A 14018 credit rejection still wins: that one is recovered by topping up.
	if got := classifyAccountFailure(403, `{"code":14018,"msg":"credits exhausted"}`); got != outcomeAccountExhausted {
		t.Errorf("classifyAccountFailure(14018) = %v, want outcomeAccountExhausted", got)
	}
}

func TestProTierBlockedMessage(t *testing.T) {
	msg := proTierBlockedMessage("gpt-6.1-sol")
	if msg == "" || !strings.Contains(msg, "gpt-6.1-sol") || !strings.Contains(msg, "Pro") {
		t.Errorf("message %q must name the model and the Pro requirement", msg)
	}
	if got := proTierBlockedMessage(""); got == "" {
		t.Error("empty model must still produce a message")
	}
}
