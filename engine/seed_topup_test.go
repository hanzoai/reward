package engine

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/rule"
)

// A program the previous seed made (v0.1.4: five levels, 94 rules, 44 badges, no
// seedVersion) is brought up to the embedded seed by Seed, and only by adding.

func previousSeed(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/seed-v0.1.4.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// stored is a store holding the program the previous seed made.
func stored(t *testing.T) (*Engine, context.Context) {
	t.Helper()
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	got, err := e.seed(ctx, previousSeed(t))
	if err != nil || got != (Seeded{Created: true}) {
		t.Fatalf("the previous seed: %+v %v", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if p.SeedVersion != 0 || p.Version != 1 || len(p.Levels) != 5 || len(p.Rules) != 94 || len(p.Badges) != 44 {
		t.Fatalf("previous program: seedVersion %d, version %d, %d levels, %d rules, %d badges", p.SeedVersion, p.Version, len(p.Levels), len(p.Rules), len(p.Badges))
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
	if want.SeedVersion != 1 {
		t.Fatalf("embedded seedVersion = %d, want 1", want.SeedVersion)
	}

	got, err := e.Seed(ctx)
	if err != nil || got != (Seeded{Rules: 30, Badges: 29, Levels: true}) {
		t.Fatalf("top-up: %+v %v, want 30 rules, 29 badges, levels replaced, upline untouched", got, err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if p.SeedVersion != 1 || p.Version != 2 || len(p.Rules) != 124 || len(p.Badges) != 73 || len(p.Levels) != 100 {
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
	if len(rec) != 1 || rec[0].Actor != "seed" || rec[0].Detail["rules"] != float64(30) || rec[0].Detail["badges"] != float64(29) || rec[0].Detail["levels"] != true || rec[0].Detail["upline"] != false || rec[0].Detail["seedVersion"] != float64(1) {
		t.Errorf("trail = %+v", rec)
	}

	// a second mount adds nothing and records nothing.
	again, err := e.Seed(ctx)
	if err != nil || again != (Seeded{}) {
		t.Errorf("second mount: %+v %v", again, err)
	}
	q, _ := e.Program(ctx, "hanzo-rewards")
	if q.SeedVersion != 1 || q.Version != 2 || len(q.Rules) != 124 || len(acts(t, e, ctx, "program.seeded")) != 1 {
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
			if !slices.Equal(p.Levels, in.Levels) || p.SeedVersion != 1 {
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
	if err := p.Validate(); err != nil || len(p.Rules) != 124 || len(p.Badges) != 73 || p.SeedVersion != 1 {
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
	if p.SeedVersion != 1 || p.Version != 1 || len(p.Rules) != 124 || len(p.Levels) != 100 {
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
