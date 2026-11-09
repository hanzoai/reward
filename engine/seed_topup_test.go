package engine

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/rule"
)

// A program the previous seed made (v0.1.4: five levels, 94 rules, 44 badges, no
// seedVersion) is brought up to the embedded seed by Seed, and only by adding.

// storedFrom is a store holding the program an earlier seed made: the document in
// testdata, created as that seed created it (no seedVersion, so 0).
func storedFrom(t *testing.T, file string) (*Engine, context.Context) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	got, err := e.seed(ctx, raw)
	if err != nil || got != (Seeded{Created: true}) {
		t.Fatalf("the seed of %s: %+v %v", file, got, err)
	}
	return e, ctx
}

// stored is a store holding the program v0.1.4 (and v0.1.5) seeded: five levels,
// 94 rules, 44 badges.
func stored(t *testing.T) (*Engine, context.Context) {
	t.Helper()
	e, ctx := storedFrom(t, "seed-v0.1.4.json")
	p, _ := e.Program(ctx, "hanzo-rewards")
	if p.SeedVersion != 0 || p.Version != 1 || len(p.Levels) != 5 || len(p.Rules) != 94 || len(p.Badges) != 44 {
		t.Fatalf("previous program: seedVersion %d, version %d, %d levels, %d rules, %d badges", p.SeedVersion, p.Version, len(p.Levels), len(p.Rules), len(p.Badges))
	}
	return e, ctx
}

// storedOldest is a store holding the program v0.1.2 seeded, whose github-badge rule
// is still once per repo (the seed now says once per repo and badge).
func storedOldest(t *testing.T) (*Engine, context.Context) {
	t.Helper()
	e, ctx := storedFrom(t, "seed-v0.1.2.json")
	p, _ := e.Program(ctx, "hanzo-rewards")
	if r := p.Find("github-badge"); p.SeedVersion != 0 || len(p.Rules) != 93 || len(p.Badges) != 44 || r == nil || r.Version != 1 || r.Limits.KeyField != "repo" {
		t.Fatalf("oldest program: seedVersion %d, %d rules, %d badges, github-badge %+v", p.SeedVersion, len(p.Rules), len(p.Badges), r)
	}
	return e, ctx
}

// header is the program's header as admin would send it back.
func header(p *rule.Program) rule.Program {
	return rule.Program{Name: p.Name, Active: p.Active, Budget: p.Budget, Upline: p.Upline, FoundingCutoff: p.FoundingCutoff,
		Levels: slices.Clone(p.Levels), Badges: slices.Clone(p.Badges), Templates: slices.Clone(p.Templates)}
}

func acts(t *testing.T, e *Engine, ctx context.Context, verb string) []*ledger.Trail {
	t.Helper()
	rows, err := ledger.Acts(ctx, e.DB, verb, "program/hanzo-rewards")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func embedded(t *testing.T) rule.Program {
	t.Helper()
	var in rule.Program
	if err := json.Unmarshal(seedJSON, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestAProgramTheLastSeedMadeIsToppedUpToTheNewOne(t *testing.T) {
	e, ctx := stored(t)
	before, _ := e.Program(ctx, "hanzo-rewards")
	want := embedded(t)
	if want.SeedVersion != 2 {
		t.Fatalf("embedded seedVersion = %d, want 2", want.SeedVersion)
	}

	got, err := e.Seed(ctx)
	if err != nil || got != (Seeded{Rules: 30, Badges: 29, Levels: true}) {
		t.Fatalf("top-up: %+v %v, want 30 rules, 29 badges, levels replaced, upline untouched", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if p.SeedVersion != 2 || p.Version != 2 || len(p.Rules) != 124 || len(p.Badges) != 73 || len(p.Levels) != 100 {
		t.Fatalf("topped up: seedVersion %d, version %d, %d rules, %d badges, %d levels", p.SeedVersion, p.Version, len(p.Rules), len(p.Badges), len(p.Levels))
	}
	if err := p.Validate(); err != nil {
		t.Errorf("the topped-up program is not valid: %v", err)
	}
	// every rule and badge of the seed is there, each as seeded (rules at version 1).
	for _, r := range want.Rules {
		r.Version = 1
		if err := r.Validate(p); err != nil { // settles what an empty field means, as saving does
			t.Fatal(err)
		}
		if have := p.Find(r.ID); have == nil || marshal(*have) != marshal(r) {
			t.Errorf("rule %s = %+v, want it as seeded", r.ID, have)
		}
	}
	for _, b := range want.Badges {
		if have := p.Badge(b.ID); have == nil || *have != b {
			t.Errorf("badge %s = %+v, want %+v", b.ID, have, b)
		}
	}
	if !slices.Equal(p.Levels, want.Levels) {
		t.Errorf("levels were not replaced with the seed's")
	}
	// what was there is as it was, in its place: the new rules and badges come after.
	for i, r := range before.Rules {
		if marshal(p.Rules[i]) != marshal(r) {
			t.Errorf("rule %s changed: %+v", r.ID, p.Rules[i])
		}
	}
	for i, b := range before.Badges {
		if p.Badges[i] != b {
			t.Errorf("badge %s changed", b.ID)
		}
	}
	if !slices.Equal(p.Upline, before.Upline) || p.Budget != before.Budget || p.Name != before.Name || p.FoundingCutoff != "" {
		t.Errorf("the header changed: %+v", p)
	}
	rec := acts(t, e, ctx, "program.seeded")
	if len(rec) != 1 || rec[0].Actor != "seed" || rec[0].Detail["rules"] != float64(30) || rec[0].Detail["badges"] != float64(29) || rec[0].Detail["levels"] != true || rec[0].Detail["upline"] != false || rec[0].Detail["seedVersion"] != float64(2) || rec[0].Detail["rulesCorrected"] != float64(0) {
		t.Errorf("trail = %+v", rec)
	}

	// a second mount adds nothing and records nothing.
	again, err := e.Seed(ctx)
	if err != nil || again != (Seeded{}) {
		t.Errorf("second mount: %+v %v", again, err)
	}
	q, _ := e.Program(ctx, "hanzo-rewards")
	if q.SeedVersion != 2 || q.Version != 2 || len(q.Rules) != 124 || len(acts(t, e, ctx, "program.seeded")) != 1 {
		t.Errorf("after the second mount: seedVersion %d, version %d, %d rules, %d records", q.SeedVersion, q.Version, len(q.Rules), len(acts(t, e, ctx, "program.seeded")))
	}
}

func TestATopUpLeavesEveryStaffEditAlone(t *testing.T) {
	e, ctx := stored(t)
	before, _ := e.Program(ctx, "hanzo-rewards")
	// staff priced a rule, switched one off, deleted one and wrote one of their own.
	priced := *before.Find("tokens-100k")
	priced.Then[0].Points = 12
	off := *before.Find("github-star")
	off.Active = false
	custom := rule.Rule{ID: "staff-made", Name: "Staff made", Active: true, When: rule.When{Event: "discord.joined"}, Then: []rule.Action{{Type: rule.Do.Points, Points: 3}}}
	for _, r := range []rule.Rule{priced, off, custom} {
		if _, err := e.SaveRule(ctx, "hanzo-rewards", r, "staff"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.DeleteRule(ctx, "hanzo-rewards", "try-dev", "staff"); err != nil {
		t.Fatal(err)
	}

	got, err := e.Seed(ctx)
	// 30 new rules, and try-dev: staff deleted it, its id is in the seed, so it is absent like a new one and comes
	// back as seeded. That is accepted: a deleted rule and one never added look the same to a top-up.
	if err != nil || got.Rules != 31 || got.Badges != 29 || !got.Levels {
		t.Fatalf("top-up: %+v %v", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if r := p.Find("tokens-100k"); r == nil || r.Then[0].Points != 12 || r.Version != 2 {
		t.Errorf("the price staff set: %+v", r)
	}
	if r := p.Find("github-star"); r == nil || r.Active || r.Version != 2 {
		t.Errorf("the rule staff switched off: %+v", r)
	}
	if r := p.Find("staff-made"); r == nil || !r.Active || r.Version != 1 {
		t.Errorf("the rule staff wrote: %+v", r)
	}
	if r := p.Find("try-dev"); r == nil || r.Version != 1 || r.Active {
		t.Errorf("the deleted rule that is in the seed: %+v", r)
	}
	if len(p.Rules) != 94+1+30 {
		t.Errorf("rules = %d, want the 94 seeded, 1 of staff's and 30 new", len(p.Rules))
	}
}

func TestATopUpLeavesALevelTableStaffChangedAlone(t *testing.T) {
	for name, edit := range map[string]func([]rule.Level) []rule.Level{
		"a renamed level":   func(l []rule.Level) []rule.Level { l[0].Name = "Newbie"; return l },
		"a moved threshold": func(l []rule.Level) []rule.Level { l[1].MinLifetimePoints = 150; return l },
		"a sixth level": func(l []rule.Level) []rule.Level {
			return append(l, rule.Level{Level: 6, Name: "Mythic", MinLifetimePoints: 50000})
		},
		"a level with a band": func(l []rule.Level) []rule.Level { l[2].Band = "Builders"; return l },
		"one level fewer":     func(l []rule.Level) []rule.Level { return l[:4] },
	} {
		t.Run(name, func(t *testing.T) {
			e, ctx := stored(t)
			cur, _ := e.Program(ctx, "hanzo-rewards")
			in := header(cur)
			in.Levels = edit(in.Levels)
			if _, err := e.SaveProgram(ctx, "hanzo-rewards", in, "staff"); err != nil {
				t.Fatal(err)
			}
			got, err := e.Seed(ctx)
			if err != nil || got.Levels || got.Rules != 30 || got.Badges != 29 {
				t.Fatalf("top-up: %+v %v", got, err)
			}
			p, _ := e.Program(ctx, "hanzo-rewards")
			if !slices.Equal(p.Levels, in.Levels) || p.SeedVersion != 2 {
				t.Errorf("levels = %+v, want staff's %+v", p.Levels, in.Levels)
			}
		})
	}
}

func TestATopUpSetsAnEmptyUplineAndNeverAStoredOne(t *testing.T) {
	for name, c := range map[string]struct {
		upline []int
		want   []int
		set    bool
	}{
		"empty is set from the seed": {nil, []int{10, 5, 2}, true},
		"a stored one stays":         {[]int{20, 0, 0}, []int{20, 0, 0}, false},
		"the seed's own stays":       {[]int{10, 5, 2}, []int{10, 5, 2}, false},
	} {
		t.Run(name, func(t *testing.T) {
			e, ctx := stored(t)
			cur, _ := e.Program(ctx, "hanzo-rewards")
			in := header(cur)
			in.Upline = c.upline
			if _, err := e.SaveProgram(ctx, "hanzo-rewards", in, "staff"); err != nil {
				t.Fatal(err)
			}
			got, err := e.Seed(ctx)
			if err != nil || got.Upline != c.set {
				t.Fatalf("top-up: %+v %v", got, err)
			}
			if p, _ := e.Program(ctx, "hanzo-rewards"); !slices.Equal(p.Upline, c.want) {
				t.Errorf("upline = %v, want %v", p.Upline, c.want)
			}
		})
	}
}

func TestNoAdminInputSetsTheSeedVersion(t *testing.T) {
	e, ctx := stored(t)
	cur, _ := e.Program(ctx, "hanzo-rewards")
	in := header(cur)
	in.SeedVersion, in.Version = 9, 99
	got, err := e.SaveProgram(ctx, "hanzo-rewards", in, "staff")
	if err != nil || got.SeedVersion != 0 || got.Version != 1 {
		t.Fatalf("a save that carries a seedVersion: %v %d %d", err, got.SeedVersion, got.Version)
	}
	// a program admin creates does not start at the seed's version either.
	made, err := e.SaveProgram(ctx, "other", rule.Program{Name: "Other", SeedVersion: 1}, "staff")
	if err != nil || made.SeedVersion != 0 {
		t.Errorf("created by admin: %v %+v", err, made)
	}
	// so the top-up still runs after staff saved the header.
	if s, err := e.Seed(ctx); err != nil || s.Rules != 30 {
		t.Errorf("top-up after a save: %+v %v", s, err)
	}
}

func TestMountsStartingTogetherLeaveEachIdOnce(t *testing.T) {
	e, ctx := stored(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Seed(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, _ := e.Program(ctx, "hanzo-rewards")
	if err := p.Validate(); err != nil || len(p.Rules) != 124 || len(p.Badges) != 73 || p.SeedVersion != 2 {
		t.Errorf("after four mounts: %v, %d rules, %d badges, seedVersion %d", err, len(p.Rules), len(p.Badges), p.SeedVersion)
	}
}

func TestAFreshStoreIsCreatedAtTheNewVersionWithNoTopUp(t *testing.T) {
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	got, err := e.Seed(ctx)
	if err != nil || got != (Seeded{Created: true}) {
		t.Fatalf("seed: %+v %v", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if p.SeedVersion != 2 || p.Version != 1 || len(p.Rules) != 124 || len(p.Levels) != 100 {
		t.Errorf("fresh: seedVersion %d, version %d, %d rules, %d levels", p.SeedVersion, p.Version, len(p.Rules), len(p.Levels))
	}
	if len(acts(t, e, ctx, "program.created")) != 1 || len(acts(t, e, ctx, "program.seeded")) != 0 {
		t.Errorf("trail: %d created, %d seeded", len(acts(t, e, ctx, "program.created")), len(acts(t, e, ctx, "program.seeded")))
	}
	if again, err := e.Seed(ctx); err != nil || again != (Seeded{}) {
		t.Errorf("second mount: %+v %v", again, err)
	}
	if q, _ := e.Program(ctx, "hanzo-rewards"); q.Version != 1 {
		t.Errorf("a second mount changed the program: version %d", q.Version)
	}
}

// badgeEvent is a README badge of a kind on a repository.
func badgeEvent(id, badge string) catalog.Event {
	return catalog.Event{ID: id, Type: "github.badge", Subject: "alice", Fields: map[string]any{"repo": "hanzoai/x", "owner": "alice", "badge": badge},
		Evidence: &catalog.Evidence{Source: "github", URL: "https://github.com/hanzoai/x", NeedsReview: true}}
}

func TestARuleNobodyEditedFollowsTheSeedAndAnEditedOneNever(t *testing.T) {
	e, ctx := storedOldest(t)
	before, _ := e.Program(ctx, "hanzo-rewards")
	// staff priced one rule and switched another off: both are at Version 2.
	priced := *before.Find("tokens-100k")
	priced.Then[0].Points = 12
	off := *before.Find("github-star")
	off.Active = false
	for _, r := range []rule.Rule{priced, off} {
		if _, err := e.SaveRule(ctx, "hanzo-rewards", r, "staff"); err != nil {
			t.Fatal(err)
		}
	}
	// a claim made under the old github-badge: once per repo, so a second kind is held.
	if out, err := e.Evaluate(ctx, badgeEvent("gb1", "profile")); err != nil || !hasClaimed(out, "github-badge") {
		t.Fatalf("gb1: %+v %v", out, err)
	}
	if out, _ := e.Evaluate(ctx, badgeEvent("gb2", "built")); hasClaimed(out, "github-badge") {
		t.Fatalf("a second badge kind on a repo was claimed before the correction: %+v", out)
	}

	edited, _ := e.Program(ctx, "hanzo-rewards") // version 3: created, then two staff saves
	got, err := e.Seed(ctx)
	// 31 rules the program lacked (30 of these releases' and one added since v0.1.2), and one corrected.
	if err != nil || got != (Seeded{Rules: 31, Badges: 29, Corrected: 1, Levels: true}) {
		t.Fatalf("top-up: %+v %v", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	want := embedded(t)
	var seeded rule.Rule
	for _, r := range want.Rules {
		if r.ID == "github-badge" {
			seeded = r
		}
	}
	if err := seeded.Validate(p); err != nil {
		t.Fatal(err)
	}
	seeded.Version = 2
	if r := p.Find("github-badge"); r == nil || marshal(*r) != marshal(seeded) || r.Limits.KeyField != "repo+badge" || r.Version != 2 {
		t.Errorf("github-badge = %+v, want the seed's at version 2", r)
	}
	// staff's rules at Version 2 are untouched: the price, and the rule that stays off.
	if r := p.Find("tokens-100k"); r == nil || r.Then[0].Points != 12 || r.Version != 2 {
		t.Errorf("the price staff set: %+v", r)
	}
	if r := p.Find("github-star"); r == nil || r.Active || r.Version != 2 {
		t.Errorf("the rule staff switched off: %+v", r)
	}
	// every other rule was identical to the seed's and is not rewritten: same content, still Version 1.
	for _, r := range before.Rules {
		if r.ID == "github-badge" || r.ID == "tokens-100k" || r.ID == "github-star" {
			continue
		}
		if have := p.Find(r.ID); have == nil || have.Version != 1 || marshal(*have) != marshal(r) {
			t.Errorf("rule %s was rewritten: %+v", r.ID, have)
		}
	}
	if p.Version != edited.Version+1 || p.SeedVersion != 2 {
		t.Errorf("version %d after %d, seedVersion %d", p.Version, edited.Version, p.SeedVersion)
	}
	rec := acts(t, e, ctx, "program.seeded")
	if len(rec) != 1 || rec[0].Detail["rulesCorrected"] != float64(1) || rec[0].Detail["rules"] != float64(31) {
		t.Errorf("trail = %+v", rec)
	}

	// the claim made before keeps the version it was made under, and its event is not claimed again;
	// another badge kind on the same repo is now a claim of its own.
	c, err := e.Claim(ctx, claim.ID("gb1", "github-badge", 1))
	if err != nil || c.RuleVersion != 1 || c.Status != claim.Pending {
		t.Errorf("the claim made before: %+v %v", c, err)
	}
	if out, _ := e.Evaluate(ctx, badgeEvent("gb1", "profile")); !hasResult(out, "github-badge", Duplicate) {
		t.Errorf("gb1 again: %+v", out)
	}
	if out, _ := e.Evaluate(ctx, badgeEvent("gb2", "built")); !hasResult(out, "github-badge", Claimed) {
		t.Errorf("gb2 after the correction: %+v", out)
	}

	// a second mount changes nothing.
	again, err := e.Seed(ctx)
	q, _ := e.Program(ctx, "hanzo-rewards")
	if err != nil || again != (Seeded{}) || q.Version != p.Version || q.Find("github-badge").Version != 2 || len(acts(t, e, ctx, "program.seeded")) != 1 {
		t.Errorf("second mount: %+v %v, version %d", again, err, q.Version)
	}
}

func hasResult(out Outcome, ruleID, result string) bool {
	for _, d := range out.Decisions {
		if d.Rule == ruleID && d.Result == result {
			return true
		}
	}
	return false
}

func hasClaimed(out Outcome, ruleID string) bool { return hasResult(out, ruleID, Claimed) }

func TestARuleStaffEditedStaysEvenIfItIsTheOneTheSeedChanged(t *testing.T) {
	e, ctx := storedOldest(t)
	cur, _ := e.Program(ctx, "hanzo-rewards")
	r := *cur.Find("github-badge")
	r.Then[0].Points = 12
	if _, err := e.SaveRule(ctx, "hanzo-rewards", r, "staff"); err != nil {
		t.Fatal(err)
	}
	got, err := e.Seed(ctx)
	if err != nil || got.Corrected != 0 {
		t.Fatalf("top-up: %+v %v", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if r := p.Find("github-badge"); r.Version != 2 || r.Then[0].Points != 12 || r.Limits.KeyField != "repo" {
		t.Errorf("github-badge = %+v, want staff's, untouched", r)
	}
}

func TestAProgramAlreadyAtSeedVersionOneIsCorrectedOnTheNextMount(t *testing.T) {
	e, ctx := storedOldest(t)
	// What v0.1.6 left on the live program: topped up to seedVersion 1, which appended and
	// corrected nothing. Seed version 1 is the embedded seed as it was, with the rule of v0.1.2.
	one := embedded(t)
	one.SeedVersion = 1
	var old rule.Program
	if err := json.Unmarshal(mustRead(t, "testdata/seed-v0.1.2.json"), &old); err != nil {
		t.Fatal(err)
	}
	for i, r := range one.Rules {
		if r.ID == "github-badge" {
			one.Rules[i] = *old.Find("github-badge")
		}
	}
	cur, _ := e.Program(ctx, "hanzo-rewards")
	if got, err := e.topUp(ctx, cur, one); err != nil || got.Corrected != 0 || got.Rules != 31 {
		t.Fatalf("the earlier top-up: %+v %v", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if r := p.Find("github-badge"); p.SeedVersion != 1 || p.Version != 2 || r.Version != 1 || r.Limits.KeyField != "repo" {
		t.Fatalf("before: seedVersion %d, version %d, github-badge %+v", p.SeedVersion, p.Version, r)
	}

	got, err := e.Seed(ctx)
	if err != nil || got != (Seeded{Corrected: 1}) {
		t.Fatalf("next mount: %+v %v, want only github-badge corrected", got, err)
	}
	p, _ = e.Program(ctx, "hanzo-rewards")
	if r := p.Find("github-badge"); p.SeedVersion != 2 || p.Version != 3 || r.Version != 2 || r.Limits.KeyField != "repo+badge" {
		t.Errorf("after: seedVersion %d, version %d, github-badge %+v", p.SeedVersion, p.Version, r)
	}
	if again, err := e.Seed(ctx); err != nil || again != (Seeded{}) {
		t.Errorf("mount after that: %+v %v", again, err)
	}
	if rec := acts(t, e, ctx, "program.seeded"); len(rec) != 2 || rec[1].Detail["rulesCorrected"] != float64(1) || rec[1].Detail["rules"] != float64(0) {
		t.Errorf("trail = %+v", rec)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
