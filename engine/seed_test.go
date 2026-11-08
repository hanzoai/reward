package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/rule"
)

func TestTheSeedCreatesTheProgramOnceAndAdminOwnsItAfter(t *testing.T) {
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	created, err := e.Seed(ctx)
	if err != nil || !created {
		t.Fatalf("seed: %v %v", created, err)
	}
	p, err := e.Program(ctx, "hanzo-rewards")
	if err != nil || !p.Active || p.Version != 1 {
		t.Fatalf("program: %v %+v", err, p)
	}

	// admin changes it; a second boot's seed changes nothing.
	r := *p.Find("tokens-100k")
	r.Then[0].Points = 12
	if _, err := e.SaveRule(ctx, "hanzo-rewards", r, "admin"); err != nil {
		t.Fatal(err)
	}
	again, err := e.Seed(ctx)
	if err != nil || again {
		t.Fatalf("second seed: %v %v", again, err)
	}
	p, _ = e.Program(ctx, "hanzo-rewards")
	if got := p.Find("tokens-100k").Then[0].Points; got != 12 {
		t.Errorf("seed overwrote admin's edit: points = %d", got)
	}
}

func TestTheSeedHoldsTheApprovedValuesAndNothingElseIsOn(t *testing.T) {
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	if _, err := e.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")

	// levels: Hacker 0, Builder 100, Shipper 500, Core 2500, Legend 10000
	wantLevels := map[string]int64{"Hacker": 0, "Builder": 100, "Shipper": 500, "Core": 2500, "Legend": 10000}
	if len(p.Levels) != len(wantLevels) {
		t.Fatalf("levels = %+v", p.Levels)
	}
	for _, l := range p.Levels {
		if wantLevels[l.Name] != l.MinLifetimePoints {
			t.Errorf("level %s = %d", l.Name, l.MinLifetimePoints)
		}
	}
	// the upline pays 10%, 5% and 2% of a claim's points to the three sponsors above.
	if !slices.Equal(p.Upline, []int{10, 5, 2}) {
		t.Errorf("upline = %v, want [10 5 2]", p.Upline)
	}
	// the one hard ceiling the spec states: $500 a month of credit.
	if p.Budget.CreditCentsPerMonth != 50_000 || p.Budget.PointsPerMonth != 0 {
		t.Errorf("budget = %+v", p.Budget)
	}

	// the earn table: rule -> points, and whether the points are credit-eligible.
	approved := map[string]struct {
		points     int64
		redeemable bool
	}{
		"tokens-100k": {10, false}, "tokens-1m": {20, false}, "tokens-5m": {30, false}, "tokens-10m": {40, false},
		"tokens-100m": {75, false}, "tokens-1b": {100, false}, "tokens-10b": {150, false}, "tokens-100b": {200, false}, "tokens-1t": {300, false},
		"referral-activated": {25, false}, "referral-retained": {25, true},
		"pr-feature": {50, true}, "pr-fix": {75, true}, "pr-docs": {30, true}, "pr-translation": {30, true},
		"issue-accepted": {10, true},
		// the owner's later values: +1 per star, follow and fork, +10 per README badge.
		"github-star": {1, false}, "github-follow": {1, false}, "github-fork": {1, false}, "github-badge": {10, false},
		"bounty-starter": {75, true}, "bounty-standard": {150, true}, "bounty-advanced": {300, true},
	}
	for id, w := range approved {
		r := p.Find(id)
		if r == nil || !r.Active {
			t.Errorf("%s: %+v", id, r)
			continue
		}
		pa := r.Then[0]
		if pa.Type != rule.Do.Points || pa.Points != w.points || pa.Redeemable != w.redeemable {
			t.Errorf("%s: %+v, want %d points redeemable=%v", id, pa, w.points, w.redeemable)
		}
	}
	// every repo and every account counts once, with no list and no cap.
	for id, key := range map[string]string{"github-star": "repo", "github-fork": "repo", "github-badge": "repo", "github-follow": "target"} {
		if l := p.Find(id).Limits; l.Once != rule.Keyed || l.KeyField != key || l.PerDay != 0 || l.PerMonth != 0 {
			t.Errorf("%s limits = %+v, want once per subject and %s only", id, l, key)
		}
	}

	// every rule is manual, and a rule that is not in the approved spec is off at 0.
	inSpec := map[string]bool{}
	for id := range approved {
		inSpec[id] = true
	}
	var off, on int
	for _, r := range p.Rules {
		if r.Approval != rule.Manual {
			t.Errorf("%s approval = %q, want manual", r.ID, r.Approval)
		}
		if r.Pillar == "" || !r.Pillar.Valid() {
			t.Errorf("%s has no pillar", r.ID)
		}
		if r.Version != 1 {
			t.Errorf("%s version = %d", r.ID, r.Version)
		}
		if inSpec[r.ID] {
			on++
			continue
		}
		off++
		if r.Active {
			t.Errorf("%s is on; it is not in the approved spec", r.ID)
		}
		for _, a := range r.Then {
			if a.Points != 0 || a.Cents != 0 {
				t.Errorf("%s carries a value: %+v", r.ID, a)
			}
		}
	}
	if on != len(inSpec) {
		t.Errorf("approved rules present = %d, want %d", on, len(inSpec))
	}

	// the owner's additions: a rule per product, beta, model family, app and expert step.
	for _, id := range []string{
		"post-dev", "post-koma", "post-zen", "post-enso", "post-kai",
		"video-cli", "video-desktop", "video-mobile",
		"test-dev", "test-machines", "test-koma", "test-uta",
		"benchmark-added", "pr-tests",
		"try-dev", "try-bot", "try-team", "try-desktop", "try-cli", "try-extension", "try-base", "try-commerce",
		"try-insights", "try-search", "try-memory", "try-machines",
		"try-koma", "try-iro", "try-mori", "try-oto", "try-koe", "try-uta",
		"try-zen", "try-enso", "try-kai",
		"expert-first-listing", "expert-verified-expert", "expert-first-engagement-delivered", "expert-five-jobs-delivered",
	} {
		r := p.Find(id)
		if r == nil {
			t.Errorf("missing rule %s", id)
			continue
		}
		if r.Active {
			t.Errorf("%s is on", id)
		}
	}
	if r := p.Find("expert-five-jobs-delivered"); r != nil {
		if r.Pillar != catalog.Expert || len(r.If) != 1 || r.If[0].Op != rule.Gte || r.If[0].Value != 4.5 {
			t.Errorf("five jobs delivered = %+v", r)
		}
	}
	// a product or model badge is once per person.
	for _, id := range []string{"try-dev", "try-koma", "try-zen"} {
		r := p.Find(id)
		if r.Limits.Once == rule.Unlimited {
			t.Errorf("%s may be earned twice", id)
		}
	}
}

func TestTheWeeklyShareRuleIsSeededOffAndKeyedByNetworkAndWeek(t *testing.T) {
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	if _, err := e.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	r := p.Find("weekly-share")
	if r == nil || r.Active || r.Approval != rule.Manual || r.Then[0].Points != 0 {
		t.Fatalf("weekly-share = %+v, want inactive, manual, 0 points", r)
	}
	if l := r.Limits; l.Once != rule.Keyed || l.KeyField != "network+week" {
		t.Errorf("limits = %+v, want once per subject, network and week", l)
	}
}
