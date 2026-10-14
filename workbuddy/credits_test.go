package main

import "testing"

func TestPackageRemainUsed_CyclePreferred(t *testing.T) {
	// Free monthly pack: lifetime CapacityUsed=0 but cycle is exhausted.
	a := resourcePackage{
		PackageName:         "CodeBuddy个人体验版",
		CapacityRemain:      500,
		CapacityUsed:        0,
		CapacitySize:        500,
		CycleCapacityRemain: 0,
		CycleCapacitySize:   500,
		// CycleCapacityUsed omitted (zero-value) — must still report used=500.
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 0 || used != 500 || size != 500 {
		t.Fatalf("cycle-exhausted: remain=%d used=%d size=%d, want 0/500/500", remain, used, size)
	}
}

func TestPackageRemainUsed_CyclePartial(t *testing.T) {
	a := resourcePackage{
		CapacityRemain:      100,
		CapacityUsed:        0,
		CapacitySize:        100,
		CycleCapacityRemain: 99,
		CycleCapacitySize:   100,
		CycleCapacityUsed:   1,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 99 || used != 1 || size != 100 {
		t.Fatalf("cycle-partial: remain=%d used=%d size=%d, want 99/1/100", remain, used, size)
	}
}

func TestPackageRemainUsed_CycleUsedOmitted(t *testing.T) {
	// CycleCapacityUsed missing; derive from size-remain.
	a := resourcePackage{
		CapacityRemain:      500,
		CapacityUsed:        0,
		CapacitySize:        500,
		CycleCapacityRemain: 499,
		CycleCapacitySize:   500,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 499 || used != 1 || size != 500 {
		t.Fatalf("cycle-used-omitted: remain=%d used=%d size=%d, want 499/1/500", remain, used, size)
	}
}

func TestPackageRemainUsed_FallbackLifetime(t *testing.T) {
	// No cycle fields → use lifetime Capacity*.
	a := resourcePackage{
		CapacityRemain: 80,
		CapacityUsed:   20,
		CapacitySize:   100,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 80 || used != 20 || size != 100 {
		t.Fatalf("lifetime: remain=%d used=%d size=%d, want 80/20/100", remain, used, size)
	}
}

func TestPackageRemainUsed_FallbackComputeUsed(t *testing.T) {
	// Lifetime Used=0 but Size>Remain → derive used.
	a := resourcePackage{
		CapacityRemain: 80,
		CapacityUsed:   0,
		CapacitySize:   100,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 80 || used != 20 || size != 100 {
		t.Fatalf("lifetime-derived: remain=%d used=%d size=%d, want 80/20/100", remain, used, size)
	}
}

func TestPackageRemainUsed_AggregateMultiPack(t *testing.T) {
	// Live shape: 体验版 exhausted + check-in packs (size grows with grants).
	// Daily check-in adds capacity; used is cycle spend, not inverse of remain.
	packs := []resourcePackage{
		{PackageName: "体验版", CapacityRemain: 500, CapacityUsed: 0, CapacitySize: 500, CycleCapacityRemain: 0, CycleCapacitySize: 500, CycleCapacityUsed: 500},
		{PackageName: "裂变包A", CapacityRemain: 99, CapacityUsed: 0, CapacitySize: 100, CycleCapacityRemain: 99, CycleCapacitySize: 100},
		{PackageName: "裂变包B", CapacityRemain: 100, CapacityUsed: 0, CapacitySize: 100, CycleCapacityRemain: 100, CycleCapacitySize: 100},
		{PackageName: "裂变包C", CapacityRemain: 100, CapacityUsed: 0, CapacitySize: 100, CycleCapacityRemain: 100, CycleCapacitySize: 100},
	}
	var totalRemain, totalUsed, totalSize int64
	for _, p := range packs {
		r, u, s := packageRemainUsed(p)
		totalRemain += r
		totalUsed += u
		totalSize += s
	}
	// remain 0+99+100+100=299; used 500+1+0+0=501; size 800
	if totalRemain != 299 || totalUsed != 501 || totalSize != 800 {
		t.Fatalf("multi-pack aggregate: remain=%d used=%d size=%d, want 299/501/800", totalRemain, totalUsed, totalSize)
	}
	// Spending 5 credits on 裂变包B: remain drops, used rises, size stable.
	packs[2].CycleCapacityRemain = 95
	totalRemain, totalUsed, totalSize = 0, 0, 0
	for _, p := range packs {
		r, u, s := packageRemainUsed(p)
		totalRemain += r
		totalUsed += u
		totalSize += s
	}
	if totalRemain != 294 || totalUsed != 506 || totalSize != 800 {
		t.Fatalf("after spend: remain=%d used=%d size=%d, want 294/506/800", totalRemain, totalUsed, totalSize)
	}
}

func TestIsCreditsExhausted(t *testing.T) {
	cases := []struct {
		name string
		cr   *creditsSummary
		want bool
	}{
		{"nil", nil, false},
		{"remain>0", &creditsSummary{TotalRemain: 10}, false},
		{"remain0 used>0", &creditsSummary{TotalRemain: 0, TotalUsed: 5}, true},
		{"remain0 size>0", &creditsSummary{TotalRemain: 0, TotalSize: 100}, true},
		{"remain0 packages", &creditsSummary{TotalRemain: 0, Packages: []packageSummary{{Name: "x"}}}, true},
		{"remain0 no data", &creditsSummary{TotalRemain: 0, TotalUsed: 0}, false},
	}
	for _, tc := range cases {
		if got := isCreditsExhausted(tc.cr); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// The live trial pack is named just "Bonus Pack" — identical to the daily
// 30-credit reward — so name matching alone never fired and the panel offered a
// claim button on accounts that already had the pack. Recognise it by shape.
func TestHasTrialPackByShape(t *testing.T) {
	cr := func(packs ...packageSummary) *creditsSummary { return &creditsSummary{Packages: packs} }
	signup := packageSummary{Name: "Bonus Pack", Size: 250, Remain: 0, CycleStart: "2026-09-30 22:04:24", CycleEnd: "2026-10-14 22:04:23"}
	daily := packageSummary{Name: "Bonus Pack", Size: 30, Remain: 30, CycleStart: "2026-10-14 01:27:32", CycleEnd: "2026-11-14 01:27:31"}
	monthly := packageSummary{Name: "Free Plan Subscription", Size: 100, Remain: 97, CycleStart: "2026-10-01 00:00:00", CycleEnd: "2026-10-31 23:59:59"}

	if !hasTrialPack(cr(daily, monthly, signup)) {
		t.Fatal("250-credit / 14-day pack must be recognised regardless of its name")
	}
	if hasTrialPack(cr(daily, monthly)) {
		t.Fatal("daily 30 and monthly 100 must not look like the trial pack")
	}
	// Same size but the wrong window (a 30-day 250-credit pack is not the trial).
	if hasTrialPack(cr(packageSummary{Name: "Bonus Pack", Size: 250, CycleStart: "2026-10-01 00:00:00", CycleEnd: "2026-10-31 00:00:00"})) {
		t.Fatal("a 30-day 250-credit pack is not the 14-day trial pack")
	}
	// Legacy name-based shapes still work.
	if !hasTrialPack(cr(packageSummary{Name: "CodeBuddy One-time Free 2-Week Pro Plan Trial"})) {
		t.Fatal("name-based detection regressed")
	}
	// CN free tier must never read as a claimed trial.
	if hasTrialPack(cr(packageSummary{Name: "CodeBuddy个人体验版", Size: 500})) {
		t.Fatal("CN free tier is not the Global trial pack")
	}
	if hasTrialPack(nil) {
		t.Fatal("nil credits must not claim a trial pack")
	}
}

func TestParsePackageTimeAcceptsAPIShapes(t *testing.T) {
	for _, raw := range []string{"2026-10-14 01:27:32", "2026-10-14T01:27:32", "2026-10-14T01:27:32Z"} {
		if _, err := parsePackageTime(raw); err != nil {
			t.Fatalf("parsePackageTime(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "not-a-time"} {
		if _, err := parsePackageTime(raw); err == nil {
			t.Fatalf("parsePackageTime(%q) should fail", raw)
		}
	}
}

// A claim that upstream answers with 14051 must be remembered, otherwise the
// button keeps offering a claim that can never succeed (the pack is gone from
// the credits list once its 14-day window ends).
func TestTrialClaimedMemoryRoundTrip(t *testing.T) {
	t.Setenv("WORKBUDDY_DATA_DIR", t.TempDir())
	if trialClaimedRemembered("auth-1") {
		t.Fatal("nothing remembered yet")
	}
	rememberTrialClaimed("auth-1")
	if !trialClaimedRemembered("auth-1") {
		t.Fatal("marker not persisted")
	}
	if trialClaimedRemembered("auth-2") {
		t.Fatal("marker leaked to another account")
	}
	rememberTrialClaimed("") // must be a no-op, not a panic
}
