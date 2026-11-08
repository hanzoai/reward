package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
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
		"referral-activated": {25, false}, "referral-retained": {25, true}, "welcome-bonus": {10, false},
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
	// each badge a README carries counts once per repository: repo and badge together.
	if l := p.Find("github-badge").Limits; l.Once != rule.Keyed || l.KeyField != "repo+badge" || l.PerDay != 0 || l.PerMonth != 0 {
		t.Errorf("github-badge limits = %+v, want once per subject, repo and badge", l)
	}
	// every repo and every account counts once, with no list and no cap.
	for id, key := range map[string]string{"github-star": "repo", "github-fork": "repo", "github-follow": "target"} {
		if l := p.Find(id).Limits; l.Once != rule.Keyed || l.KeyField != key || l.PerDay != 0 || l.PerMonth != 0 {
			t.Errorf("%s limits = %+v, want once per subject and %s only", id, l, key)
		}
	}

	// every rule is manual, and a rule that is not in the approved spec is off at 0,
	// except the owner's later additions (TestTheSeedHoldsTheTenureStreakFounderAndReferralRules).
	inSpec := map[string]bool{}
	for id := range approved {
		inSpec[id] = true
	}
	later := map[string]bool{"founding-developer": true, "referral-network": true}
	for _, c := range counted {
		later[c.rule] = true
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
		if later[r.ID] {
			if !r.Active {
				t.Errorf("%s is off; the owner approved it", r.ID)
			}
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

// counted are the badge-only rules the owner added: one badge when an event carries
// exactly this number. Their badges are in the program's list under the same pillar
// as the rule, and a common one has no rarity written.
var counted = []struct {
	rule, event, field string
	n                  int
	badge, name        string
	rarity             rule.Rarity
	pillar             catalog.Pillar
}{
	{"tenure-1", "tenure.reached", "months", 1, "one-month", "One Month", rule.Common, catalog.Use},
	{"tenure-6", "tenure.reached", "months", 6, "six-months", "Six Months", rule.Uncommon, catalog.Use},
	{"tenure-12", "tenure.reached", "months", 12, "yearling", "Yearling", rule.Rare, catalog.Use},
	{"tenure-24", "tenure.reached", "months", 24, "two-years", "Two Years", rule.Epic, catalog.Use},
	{"tenure-36", "tenure.reached", "months", 36, "three-years", "Three Years", rule.Legendary, catalog.Use},
	{"streak-7", "streak.reached", "days", 7, "week-streak", "Week Streak", rule.Common, catalog.Use},
	{"streak-30", "streak.reached", "days", 30, "month-streak", "Month Streak", rule.Rare, catalog.Use},
	{"streak-100", "streak.reached", "days", 100, "hundred-days", "Hundred Days", rule.Epic, catalog.Use},
	{"referral-milestone-5", "referral.milestone", "count", 5, "spark", "Spark", rule.Common, catalog.Share},
	{"referral-milestone-10", "referral.milestone", "count", 10, "signal", "Signal", rule.Common, catalog.Share},
	{"referral-milestone-15", "referral.milestone", "count", 15, "beacon", "Beacon", rule.Uncommon, catalog.Share},
	{"referral-milestone-20", "referral.milestone", "count", 20, "current", "Current", rule.Uncommon, catalog.Share},
	{"referral-milestone-25", "referral.milestone", "count", 25, "catalyst", "Catalyst", rule.Rare, catalog.Share},
	{"referral-milestone-30", "referral.milestone", "count", 30, "amplifier", "Amplifier", rule.Rare, catalog.Share},
	{"referral-milestone-35", "referral.milestone", "count", 35, "orbit", "Orbit", rule.Epic, catalog.Share},
	{"referral-milestone-40", "referral.milestone", "count", 40, "constellation", "Constellation", rule.Epic, catalog.Share},
	{"referral-milestone-45", "referral.milestone", "count", 45, "nova", "Nova", rule.Epic, catalog.Share},
	{"referral-milestone-50", "referral.milestone", "count", 50, "supernova", "Supernova", rule.Legendary, catalog.Share},
	{"referral-milestone-100", "referral.milestone", "count", 100, "cosmos-i", "Cosmos I", rule.Legendary, catalog.Share},
	{"referral-milestone-150", "referral.milestone", "count", 150, "cosmos-ii", "Cosmos II", rule.Legendary, catalog.Share},
	{"referral-milestone-200", "referral.milestone", "count", 200, "cosmos-iii", "Cosmos III", rule.Legendary, catalog.Share},
	{"referral-milestone-250", "referral.milestone", "count", 250, "cosmos-iv", "Cosmos IV", rule.Legendary, catalog.Share},
	{"referral-milestone-300", "referral.milestone", "count", 300, "cosmos-v", "Cosmos V", rule.Legendary, catalog.Share},
	{"referral-milestone-350", "referral.milestone", "count", 350, "cosmos-vi", "Cosmos VI", rule.Legendary, catalog.Share},
	{"referral-milestone-400", "referral.milestone", "count", 400, "cosmos-vii", "Cosmos VII", rule.Legendary, catalog.Share},
	{"referral-milestone-450", "referral.milestone", "count", 450, "cosmos-viii", "Cosmos VIII", rule.Legendary, catalog.Share},
	{"referral-milestone-500", "referral.milestone", "count", 500, "cosmos-ix", "Cosmos IX", rule.Legendary, catalog.Share},
	{"referral-milestone-550", "referral.milestone", "count", 550, "cosmos-x", "Cosmos X", rule.Legendary, catalog.Share},
}

func TestTheSeedHoldsTheTenureStreakFounderAndReferralRules(t *testing.T) {
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	if _, err := e.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	for _, c := range counted {
		r := p.Find(c.rule)
		if r == nil {
			t.Errorf("missing rule %s", c.rule)
			continue
		}
		if !r.Active || r.Approval != rule.Manual || r.Version != 1 || r.Pillar != c.pillar || r.When.Event != c.event {
			t.Errorf("%s = %+v, want active, manual, version 1, pillar %s on %s", c.rule, r, c.pillar, c.event)
		}
		if len(r.If) != 1 || r.If[0] != (rule.Condition{Field: c.field, Op: rule.Eq, Value: float64(c.n)}) {
			t.Errorf("%s if = %+v, want %s eq %d", c.rule, r.If, c.field, c.n)
		}
		// badge only: the one action is the badge, with no points action beside it.
		if len(r.Then) != 1 || r.Then[0] != (rule.Action{Type: rule.Do.Badge, Badge: c.badge}) {
			t.Errorf("%s then = %+v, want only the badge %s", c.rule, r.Then, c.badge)
		}
		if r.Limits != (rule.Limits{Once: rule.Keyed, KeyField: c.field}) {
			t.Errorf("%s limits = %+v, want once per subject and %s", c.rule, r.Limits, c.field)
		}
		b := p.Badge(c.badge)
		if b == nil || b.Name != c.name || b.Pillar != c.pillar || cmp.Or(b.Rarity, rule.Common) != c.rarity {
			t.Errorf("%s: badge = %+v, want %s %s %s", c.rule, b, c.name, c.pillar, c.rarity)
		}
	}

	f := p.Find("founding-developer")
	if f == nil || !f.Active || f.Approval != rule.Manual || f.Pillar != catalog.Community || f.When.Event != "founder.eligible" || len(f.If) != 0 ||
		len(f.Then) != 1 || f.Then[0] != (rule.Action{Type: rule.Do.Badge, Badge: "founding-developer"}) || f.Limits != (rule.Limits{Once: rule.Subject}) {
		t.Errorf("founding-developer = %+v", f)
	}
	if b := p.Badge("founding-developer"); b == nil || b.Name != "Founding Developer" || b.Pillar != catalog.Community || b.Rarity != rule.Legendary {
		t.Errorf("founding-developer badge = %+v", b)
	}

	n := p.Find("referral-network")
	if n == nil || !n.Active || n.Approval != rule.Manual || n.Pillar != catalog.Share || n.When.Event != "referral.network" || len(n.If) != 0 ||
		len(n.Then) != 1 || n.Then[0] != (rule.Action{Type: rule.Do.Points, Points: 1}) || n.Limits != (rule.Limits{Once: rule.Keyed, KeyField: "referee"}) {
		t.Errorf("referral-network = %+v", n)
	}

	// the Cosmos series is ten badges, I to X, at every 50 referrals from 100 to 550.
	var cosmos []string
	for _, c := range counted {
		if strings.HasPrefix(c.badge, "cosmos-") {
			cosmos = append(cosmos, c.name)
			if c.n < 100 || c.n > 550 || c.n%50 != 0 {
				t.Errorf("%s is at %d", c.name, c.n)
			}
		}
	}
	if !slices.Equal(cosmos, []string{"Cosmos I", "Cosmos II", "Cosmos III", "Cosmos IV", "Cosmos V", "Cosmos VI", "Cosmos VII", "Cosmos VIII", "Cosmos IX", "Cosmos X"}) {
		t.Errorf("cosmos = %v", cosmos)
	}

	if len(counted) != 28 {
		t.Errorf("counted rules = %d, want 8 + 20", len(counted))
	}
	// the program knows nothing of a founding cutoff: the owner sets the date.
	if p.FoundingCutoff != "" {
		t.Errorf("seeded foundingCutoff = %q, want none", p.FoundingCutoff)
	}
}

func TestTheSeedHasHundredLevelsInTenBands(t *testing.T) {
	ctx := context.Background()
	e := &Engine{DB: openDB(t)}
	if _, err := e.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Program(ctx, "hanzo-rewards")
	if len(p.Levels) != 100 {
		t.Fatalf("levels = %d, want 100", len(p.Levels))
	}
	bands := []string{"Photon", "Wave", "Focus", "Flare", "Comet", "Pulsar", "Nebula", "Quasar", "Magnetar", "Singularity"}
	for n, l := range p.Levels {
		band := bands[n/10]
		if l.Level != n || l.MinLifetimePoints != int64(10*n*n) || l.Band != band || l.Name != fmt.Sprintf("%s %d", band, n) {
			t.Errorf("level %d = %+v, want %s %d at %d points", n, l, band, n, 10*n*n)
		}
	}
	// the thresholds the owner named, and the bands at the boundaries.
	for _, c := range []struct {
		n      int
		points int64
		name   string
	}{{0, 0, "Photon 0"}, {1, 10, "Photon 1"}, {9, 810, "Photon 9"}, {10, 1000, "Wave 10"}, {49, 24010, "Comet 49"}, {50, 25000, "Pulsar 50"}, {99, 98010, "Singularity 99"}} {
		if l := p.Levels[c.n]; l.MinLifetimePoints != c.points || l.Name != c.name {
			t.Errorf("level %d = %+v, want %s at %d", c.n, l, c.name, c.points)
		}
	}
	// one point short of a level is the level below.
	for _, c := range []struct {
		points int64
		level  int
		name   string
		band   string
	}{
		{0, 0, "Photon 0", "Photon"}, {9, 0, "Photon 0", "Photon"}, {10, 1, "Photon 1", "Photon"},
		{999, 9, "Photon 9", "Photon"}, {1000, 10, "Wave 10", "Wave"},
		{24_999, 49, "Comet 49", "Comet"}, {25_000, 50, "Pulsar 50", "Pulsar"},
		{98_009, 98, "Singularity 98", "Singularity"}, {98_010, 99, "Singularity 99", "Singularity"}, {10_000_000, 99, "Singularity 99", "Singularity"},
	} {
		l, next := p.LevelOf(c.points)
		if l.Level != c.level || l.Name != c.name || l.Band != c.band {
			t.Errorf("%d points: %+v, want %s (%s)", c.points, l, c.name, c.band)
		}
		if (next == nil) != (c.level == 99) || (next != nil && next.Level != c.level+1) {
			t.Errorf("%d points: next = %+v", c.points, next)
		}
	}
}
