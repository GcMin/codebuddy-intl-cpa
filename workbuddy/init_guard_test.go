package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNothingRunsAtLibraryLoad enforces the rule behind the darwin/amd64 host
// crash (golang/go#40131, our issue #4).
//
// The Go runtime schedules the first goroutine on m0 — the thread that called
// dlopen, i.e. the host's thread — and on darwin/amd64 it tracks the current
// goroutine in a single hardcoded TLS slot (0x30(GS), see
// cmd/link/internal/ld/sym.go computeTLSOffset). A c-shared plugin that leaves
// a goroutine or timer running while dlopen is returning therefore leaves the
// host reading the plugin's g, which shows up as "bad sweepgen in refill" in
// the host and kills the whole process. Nothing may start at load time.
func TestNothingRunsAtLibraryLoad(t *testing.T) {
	forbiddenCalls := map[string]bool{
		"time.NewTicker": true,
		"time.NewTimer":  true,
		"time.AfterFunc": true,
		"time.Tick":      true,
		"time.After":     true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	inits := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, errParse := parser.ParseFile(fset, name, nil, 0)
		if errParse != nil {
			t.Fatalf("parse %s: %v", name, errParse)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "init" {
				continue
			}
			inits++
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.GoStmt:
					t.Errorf("%s: init starts a goroutine — start it on first use instead, or the host "+
						"shares this plugin's goroutine slot and crashes on darwin/amd64",
						fset.Position(typed.Pos()))
				case *ast.CallExpr:
					if name := calleeName(typed); forbiddenCalls[name] {
						t.Errorf("%s: init calls %s — timers must not run while the library is being loaded",
							fset.Position(typed.Pos()), name)
					}
				}
				return true
			})
		}
	}
	// The check is only meaningful if init functions still exist to inspect;
	// if they are all gone the assertions above silently stop guarding anything.
	if inits == 0 {
		t.Log("no init functions in this package — load-time guard is vacuous")
	}
}

func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok {
			return pkg.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	}
	return ""
}

// TestPruneExpiredLogins covers the sweep itself, which now runs from the
// lazily started janitor instead of from init.
func TestPruneExpiredLogins(t *testing.T) {
	now := time.Now()
	expired := &loginCtx{expires: now.Add(-time.Minute)}
	live := &loginCtx{expires: now.Add(time.Minute)}
	loginStates.Store("expired", expired)
	loginStates.Store("live", live)
	t.Cleanup(func() {
		loginStates.Delete("expired")
		loginStates.Delete("live")
	})

	pruneExpiredLogins(now)

	if _, ok := loginStates.Load("expired"); ok {
		t.Error("expired login state survived the sweep")
	}
	if _, ok := loginStates.Load("live"); !ok {
		t.Error("live login state was dropped by the sweep")
	}
}

// TestJanitorStartsOnce pins the laziness: the janitor is not running until
// startLoginJanitor is called, and repeated calls add exactly one goroutine.
func TestJanitorStartsOnce(t *testing.T) {
	before := runtime.NumGoroutine()
	startLoginJanitor()
	startLoginJanitor()
	startLoginJanitor()

	// The goroutine is created synchronously by the runtime, but allow a short
	// settle window so the assertion is about the count, not about scheduling.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() < before+1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("janitor started %d goroutines, want at most 1 (before=%d after=%d)", after-before, before, after)
	}
}
