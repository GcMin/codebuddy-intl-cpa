package main

import (
	"os"
	"os/exec"
	"testing"
)

// GH #14: a region where www.codebuddy.ai is DNS-blackholed can move the whole
// Global surface to the realm's other host without touching the credential.
func TestResolveGlobalBase(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"unset", "", defaultUpstreamBaseGlobal},
		{"blank", "   ", defaultUpstreamBaseGlobal},
		{"override", "https://www.workbuddy.ai", "https://www.workbuddy.ai"},
		{"trailing slash", "https://www.workbuddy.ai/", "https://www.workbuddy.ai"},
		{"http", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"no scheme", "www.workbuddy.ai", defaultUpstreamBaseGlobal},
		{"bad scheme", "ftp://www.workbuddy.ai", defaultUpstreamBaseGlobal},
		{"garbage", "://nope", defaultUpstreamBaseGlobal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WB_UPSTREAM_GLOBAL", tc.env)
			if got := resolveGlobalBase(); got != tc.want {
				t.Fatalf("resolveGlobalBase() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every derived Global base must follow the same resolved value, otherwise an
// override moves chat but leaves login or billing pointed at the dead host.
func TestGlobalBaseDerivatives(t *testing.T) {
	if originRefererGlobal != upstreamBaseGlobal {
		t.Fatalf("originRefererGlobal = %q, want %q", originRefererGlobal, upstreamBaseGlobal)
	}
	if billingBaseGlobal != upstreamBaseGlobal {
		t.Fatalf("billingBaseGlobal = %q, want %q", billingBaseGlobal, upstreamBaseGlobal)
	}
	if loginBaseGlobal != upstreamBaseGlobal {
		t.Fatalf("loginBaseGlobal = %q, want %q", loginBaseGlobal, upstreamBaseGlobal)
	}
	if mmUpstreamBaseGlobal != upstreamBaseGlobal {
		t.Fatalf("mmUpstreamBaseGlobal = %q, want %q", mmUpstreamBaseGlobal, upstreamBaseGlobal)
	}
}

// The override is read at package init, so it can only be observed in a fresh
// process. The child also guards every derived base: a missed initializer
// (billingBaseGlobal did, until 0.10.0) silently leaves one surface on the
// dead host while the changelog claims otherwise.
func TestGlobalBaseOverrideAtInit(t *testing.T) {
	const override = "https://www.workbuddy.ai"
	if os.Getenv("WB_INIT_CHILD") == "1" {
		for name, got := range map[string]string{
			"upstreamBaseGlobal":   upstreamBaseGlobal,
			"originRefererGlobal":  originRefererGlobal,
			"billingBaseGlobal":    billingBaseGlobal,
			"loginBaseGlobal":      loginBaseGlobal,
			"mmUpstreamBaseGlobal": mmUpstreamBaseGlobal,
		} {
			if got != override {
				t.Fatalf("%s = %q at init, want %q", name, got, override)
			}
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGlobalBaseOverrideAtInit$")
	cmd.Env = append(os.Environ(), "WB_INIT_CHILD=1", "WB_UPSTREAM_GLOBAL="+override)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init override not applied: %v\n%s", err, out)
	}
}
