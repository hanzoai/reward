package catalog

import (
	"errors"
	"slices"
	"testing"

	"github.com/hanzoai/reward/refuse"
)

func TestCatalogListsEveryEventUnderAPillar(t *testing.T) {
	groups := Lookup()
	if len(groups) != 5 {
		t.Fatalf("pillars = %d, want the five", len(groups))
	}
	order := []Pillar{Use, Share, Community, Contribute, Expert}
	n := 0
	for i, g := range groups {
		if g.ID != order[i] {
			t.Fatalf("pillar %d = %s, want %s", i, g.ID, order[i])
		}
		for _, e := range g.Events {
			if e.Pillar != g.ID {
				t.Errorf("%s is listed under %s but belongs to %s", e.Name, g.ID, e.Pillar)
			}
			n++
		}
	}
	if n != len(events) {
		t.Errorf("grouped %d events of %d", n, len(events))
	}
	want := map[string]Pillar{
		"usage.tokens": Use, "product.used": Use, "model.used": Use,
		"referral.activated": Share, "referral.retained": Share, "referral.linked": Share, "social.post.verified": Share,
		"discord.joined": Community, "github.star": Community, "github.follow": Community,
		"github.fork": Contribute, "github.badge": Contribute,
		"github.issue.accepted": Contribute, "github.pr.merged": Contribute, "submission.video": Contribute,
		"submission.test": Contribute, "benchmark.added": Contribute, "bounty.completed": Contribute, "deploy.button": Contribute,
		"expert.listed": Expert, "expert.verified": Expert, "expert.job.delivered": Expert, "expert.review": Expert,
	}
	for name, pillar := range want {
		e, ok := Type(name)
		if !ok || e.Pillar != pillar {
			t.Errorf("%s: in catalog %v, pillar %s, want %s", name, ok, e.Pillar, pillar)
		}
	}
	if len(want) != len(events) {
		t.Errorf("catalog has %d events, the table here %d", len(events), len(want))
	}
	pr, _ := Type("github.pr.merged")
	k, _ := pr.Field("kind")
	if k.Kind != Choice || !slices.Equal(k.Values, []string{"fix", "feature", "docs", "tests", "translation"}) {
		t.Errorf("github.pr.merged kind = %+v", k)
	}
	job, _ := Type("expert.job.delivered")
	if f, _ := job.Field("rating"); f.Kind != Real {
		t.Errorf("rating = %+v, want a real number", f)
	}
	linked, ok := Type("referral.linked")
	if !ok || linked.Title != "Referral linked" || linked.Evidence || len(linked.Fields) != 1 || linked.Fields[0].Name != "sponsor" || linked.Fields[0].Kind != Text {
		t.Errorf("referral.linked = %+v", linked)
	}
	fork, _ := Type("github.fork")
	if len(fork.Fields) != 2 || fork.Fields[0].Name != "repo" || fork.Fields[1].Name != "forker" {
		t.Errorf("github.fork fields = %+v", fork.Fields)
	}
	b, _ := Type("github.badge")
	if f, _ := b.Field("badge"); f.Kind != Choice || !slices.Equal(f.Values, []string{"built", "deploy"}) {
		t.Errorf("github.badge badge = %+v", f)
	}
}

func TestAnEventMustBeExactlyWhatTheCatalogLists(t *testing.T) {
	ok := func() Event {
		return Event{ID: "e", Type: "github.pr.merged", Subject: "u", Fields: map[string]any{"repo": "r", "labels": []any{"bug"}, "kind": "fix"}, Evidence: &Evidence{Source: "github", URL: "https://github.com/hanzoai/x", NeedsReview: true}}
	}
	cases := map[string]func(*Event){
		"unknown type":   func(e *Event) { e.Type = "github.pr.closed" },
		"no id":          func(e *Event) { e.ID = " " },
		"no subject":     func(e *Event) { e.Subject = "" },
		"missing field":  func(e *Event) { delete(e.Fields, "repo") },
		"extra field":    func(e *Event) { e.Fields["draft"] = true },
		"wrong kind":     func(e *Event) { e.Fields["repo"] = 7 },
		"not a choice":   func(e *Event) { e.Fields["kind"] = "refactor" },
		"labels not str": func(e *Event) { e.Fields["labels"] = []any{1} },
		"long id":        func(e *Event) { e.ID = string(make([]byte, 300)) },
	}
	for name, mod := range cases {
		e := ok()
		mod(&e)
		if err := e.Validate(); !refuse.Is(err) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	e := ok()
	if err := e.Validate(); err != nil {
		t.Fatalf("a good event: %v", err)
	}
	// numbers arrive as float64 from JSON and as int from Go; a whole field refuses a fraction.
	tok := Event{ID: "e", Type: "usage.tokens", Subject: "u", Fields: map[string]any{"tokens": 100000, "model": "zen4"}}
	if err := tok.Validate(); err != nil || tok.Fields["tokens"] != float64(100000) {
		t.Errorf("int tokens: %v %v", err, tok.Fields["tokens"])
	}
	tok.Fields["tokens"] = 1.5
	if err := tok.Validate(); err == nil {
		t.Error("a fraction of a token was taken")
	}
	var bad error = refuse.New("x")
	if !errors.Is(bad, bad) || !refuse.Is(bad) {
		t.Error("refuse.Is")
	}
}
