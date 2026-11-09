package rule

import (
	"testing"
	"time"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/refuse"
)

func TestConditionsMatch(t *testing.T) {
	pr := catalog.Event{ID: "e", Type: "github.pr.merged", Subject: "u", Fields: map[string]any{
		"repo": "hanzoai/Commerce", "labels": []any{"Bug", "p1"}, "kind": "fix"}, Evidence: &catalog.Evidence{Source: "github", URL: "https://github.com/hanzoai/x", NeedsReview: true}}
	tok := catalog.Event{ID: "e", Type: "usage.tokens", Subject: "u", Fields: map[string]any{"tokens": 5_000_000, "model": "zen4-mini"}}
	job := catalog.Event{ID: "e", Type: "expert.job.delivered", Subject: "u", Fields: map[string]any{"job": "j1", "rating": 4.5}}
	test := catalog.Event{ID: "e", Type: "submission.test", Subject: "u", Fields: map[string]any{"product": "koma", "beta": true}}
	for _, e := range []*catalog.Event{&pr, &tok, &job, &test} {
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		ev   catalog.Event
		c    Condition
		want bool
	}{
		{"eq text", pr, Condition{"repo", Eq, "hanzoai/Commerce"}, true},
		{"eq text is exact", pr, Condition{"repo", Eq, "hanzoai/commerce"}, false},
		{"ne", pr, Condition{"kind", Ne, "docs"}, true},
		{"ne equal", pr, Condition{"kind", Ne, "fix"}, false},
		{"in", pr, Condition{"kind", In, []any{"docs", "fix"}}, true},
		{"in miss", pr, Condition{"kind", In, []any{"docs", "tests"}}, false},
		{"contains label ignores case", pr, Condition{"labels", Contains, "bug"}, true},
		{"contains label miss", pr, Condition{"labels", Contains, "docs"}, false},
		{"contains substring", tok, Condition{"model", Contains, "ZEN"}, true},
		{"contains substring miss", tok, Condition{"model", Contains, "kai"}, false},
		{"gte equal", tok, Condition{"tokens", Gte, 5_000_000}, true},
		{"gte above", tok, Condition{"tokens", Gte, 5_000_001}, false},
		{"lte equal", tok, Condition{"tokens", Lte, 5_000_000}, true},
		{"lte below", tok, Condition{"tokens", Lte, 4_999_999}, false},
		{"rating gte 4.5", job, Condition{"rating", Gte, 4.5}, true},
		{"rating gte 4.6", job, Condition{"rating", Gte, 4.6}, false},
		{"flag eq", test, Condition{"beta", Eq, true}, true},
		{"flag eq false", test, Condition{"beta", Eq, false}, false},
		{"numbers in", tok, Condition{"tokens", In, []any{1, 5_000_000}}, true},
	}
	for _, tc := range cases {
		r := Rule{ID: "r", When: When{Event: tc.ev.Type}, If: []Condition{tc.c}}
		if got, why := r.Matches(tc.ev); got != tc.want {
			t.Errorf("%s: matched %v (%s), want %v", tc.name, got, why, tc.want)
		}
	}
	// every condition must hold
	r := Rule{ID: "r", When: When{Event: "github.pr.merged"}, If: []Condition{{"kind", Eq, "fix"}, {"labels", Contains, "docs"}}}
	if ok, why := r.Matches(pr); ok || why == "" {
		t.Errorf("a rule with one failing condition matched (%q)", why)
	}
	// another event type never matches
	if ok, _ := r.Matches(tok); ok {
		t.Error("a rule matched an event of another type")
	}
}

func TestARuleIsCheckedAgainstTheCatalog(t *testing.T) {
	p := &Program{Badges: []Badge{{ID: "b1", Name: "One", Pillar: catalog.Use}}, Templates: []Template{{ID: "hello", Subject: "s", Body: "b"}}}
	good := func() Rule {
		return Rule{ID: "ok", Name: "ok", When: When{Event: "github.pr.merged"}, Then: []Action{{Type: Do.Badge, Badge: "b1"}}}
	}
	cases := map[string]func(*Rule){
		"bad id":                    func(r *Rule) { r.ID = "Not A Slug" },
		"no name":                   func(r *Rule) { r.Name = "" },
		"unknown event":             func(r *Rule) { r.When.Event = "github.nope" },
		"bad pillar":                func(r *Rule) { r.Pillar = "fun" },
		"bad approval":              func(r *Rule) { r.Approval = "maybe" },
		"unknown field":             func(r *Rule) { r.If = []Condition{{"stars", Eq, 1}} },
		"unknown op":                func(r *Rule) { r.If = []Condition{{"repo", "like", "x"}} },
		"op not for kind":           func(r *Rule) { r.If = []Condition{{"repo", Gte, 1}} },
		"eq on a list":              func(r *Rule) { r.If = []Condition{{"labels", Eq, "x"}} },
		"value of wrong kind":       func(r *Rule) { r.If = []Condition{{"repo", Eq, 5}} },
		"choice not offered":        func(r *Rule) { r.If = []Condition{{"kind", Eq, "refactor"}} },
		"in needs a list":           func(r *Rule) { r.If = []Condition{{"kind", In, "fix"}} },
		"no actions":                func(r *Rule) { r.Then = nil },
		"badge not in program":      func(r *Rule) { r.Then = []Action{{Type: Do.Badge, Badge: "zz"}} },
		"template missing":          func(r *Rule) { r.Then = []Action{{Type: Do.Email, Template: "nope"}} },
		"negative points":           func(r *Rule) { r.Then = []Action{{Type: Do.Points, Points: -1}} },
		"credit of nothing":         func(r *Rule) { r.Then = []Action{{Type: Do.Credit}} },
		"revshare is not an action": func(r *Rule) { r.Then = []Action{{Type: "revshare"}} },
		"payout is not an action":   func(r *Rule) { r.Then = []Action{{Type: "payout"}} },
		"once keyed needs key":      func(r *Rule) { r.Limits = Limits{Once: Keyed} },
		"key not a field":           func(r *Rule) { r.Limits = Limits{Once: Keyed, KeyField: "stars"} },
		"unknown once":              func(r *Rule) { r.Limits = Limits{Once: "twice"} },
		"negative perDay":           func(r *Rule) { r.Limits = Limits{PerDay: -1} },
	}
	for name, mod := range cases {
		r := good()
		mod(&r)
		if err := r.Validate(p); !refuse.Is(err) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	r := good()
	if err := r.Validate(p); err != nil {
		t.Fatal(err)
	}
	if r.Approval != Manual {
		t.Errorf("approval defaults to %q, want manual", r.Approval)
	}
	if r.Pillar != catalog.Contribute {
		t.Errorf("pillar defaults to %q, want the event's, contribute", r.Pillar)
	}
}

func TestEveryRuleAndBadgeHasAPillarAndTheVocabularyIsComplete(t *testing.T) {
	w := Vocabulary()
	if len(w.Ops) != 6 || len(w.Actions) != 4 || len(w.Approvals) != 2 || len(w.Onces) != 3 {
		t.Errorf("vocabulary = %+v", w)
	}
	p := &Program{Name: "x", Badges: []Badge{{ID: "b", Name: "B", Pillar: ""}}}
	p.SetId(ProgramID("x"))
	if err := p.Validate(); !refuse.Is(err) {
		t.Errorf("a badge with no pillar: %v", err)
	}
	p.Badges[0].Pillar = catalog.Share
	p.Badges[0].Rarity = "mythic"
	if err := p.Validate(); !refuse.Is(err) {
		t.Errorf("a badge with a made-up rarity: %v", err)
	}
	p.Badges[0].Rarity = Epic
	if err := p.Validate(); err != nil {
		t.Errorf("a good badge: %v", err)
	}
}

func TestLevelsAreNumberedFromZeroOrOneInOrder(t *testing.T) {
	levels := func(first int, mins ...int64) []Level {
		var out []Level
		for i, m := range mins {
			out = append(out, Level{Level: first + i, Name: "L", MinLifetimePoints: m})
		}
		return out
	}
	for name, l := range map[string][]Level{
		"none":                  nil,
		"from 1":                levels(1, 0, 100, 500),
		"from 0":                levels(0, 0, 10, 40),
		"a single level from 0": levels(0, 0),
	} {
		p := &Program{Name: "x", Levels: l}
		p.SetId(ProgramID("x"))
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, l := range map[string][]Level{
		"from 2":            levels(2, 0, 100),
		"from -1":           levels(-1, 0, 100),
		"a gap":             {{Level: 0, Name: "L"}, {Level: 2, Name: "L", MinLifetimePoints: 10}},
		"repeated":          {{Level: 0, Name: "L"}, {Level: 0, Name: "L", MinLifetimePoints: 10}},
		"the first above 0": levels(0, 5, 10),
		"not rising":        levels(0, 0, 10, 10),
		"falling":           levels(1, 0, 100, 50),
		"no name":           {{Level: 0, Name: " "}},
	} {
		p := &Program{Name: "x", Levels: l}
		p.SetId(ProgramID("x"))
		if err := p.Validate(); !refuse.Is(err) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

func TestALevelWithNoBandIsItsOwnBand(t *testing.T) {
	p := &Program{Levels: []Level{{Level: 1, Name: "Hacker"}, {Level: 2, Name: "Builder 2", Band: "Builder", MinLifetimePoints: 100}}}
	at, next := p.LevelOf(0)
	if at.Band != "Hacker" || next == nil || next.Band != "Builder" {
		t.Errorf("0 points: %+v then %+v", at, next)
	}
	if at, next = p.LevelOf(100); at.Band != "Builder" || at.Name != "Builder 2" || next != nil {
		t.Errorf("100 points: %+v then %+v", at, next)
	}
	// the program's own table is left as it was written.
	if p.Levels[0].Band != "" {
		t.Errorf("LevelOf wrote the band into the program: %+v", p.Levels)
	}
	if at, next = (&Program{}).LevelOf(5); at != (Level{}) || next != nil {
		t.Errorf("a program with no levels: %+v then %+v", at, next)
	}
}

func TestTheFoundingCutoffIsADateOrATimestampOrEmpty(t *testing.T) {
	for cutoff, want := range map[string]time.Time{
		"":                          {},
		"2026-01-01":                time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		"2026-01-01T00:00:00Z":      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		"2026-01-01T09:30:00-08:00": time.Date(2026, 1, 1, 17, 30, 0, 0, time.UTC),
	} {
		p := &Program{Name: "x", FoundingCutoff: cutoff}
		p.SetId(ProgramID("x"))
		if err := p.Validate(); err != nil {
			t.Errorf("%q: %v", cutoff, err)
		}
		if got, err := p.Cutoff(); err != nil || !got.Equal(want) {
			t.Errorf("%q = %v %v, want %v", cutoff, got, err, want)
		}
	}
	for _, cutoff := range []string{"soon", "2026-13-01", "2026-02-30", "01/02/2026", "2026-01-01 00:00:00", " 2026-01-01", "2026", "2026-01-01T00:00:00"} {
		p := &Program{Name: "x", FoundingCutoff: cutoff}
		p.SetId(ProgramID("x"))
		if err := p.Validate(); !refuse.Is(err) {
			t.Errorf("%q: err = %v, want a refusal", cutoff, err)
		}
	}
}
