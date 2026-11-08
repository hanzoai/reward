package engine

import (
	"errors"
	"testing"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/profile"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

func TestAProfileIsPrivateUntilItsOwnerTurnsItOn(t *testing.T) {
	r := newRig(t, nil)
	if p, err := r.e.Profile(r.ctx, "alice"); err != nil || p.Public || p.Handle != "" {
		t.Fatalf("a new subject's profile: %v %+v", err, p)
	}
	if _, err := r.e.Show(r.ctx, "alice", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a handle nobody holds: %v", err)
	}
	// a handle is held, but the profile is private: the same answer as no such handle.
	if _, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "alice", Public: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Show(r.ctx, "alice", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a private profile: %v", err)
	}
	if _, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "alice", Public: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Show(r.ctx, "Alice", ""); err != nil {
		t.Errorf("a public profile, any case: %v", err)
	}
	if _, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "alice", Public: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Show(r.ctx, "alice", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a profile turned off again: %v", err)
	}
}

func TestAHandleBelongsToOneSubject(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "zed", Public: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.SaveProfile(r.ctx, "bob", profile.Profile{Handle: "ZED", Public: true}); !errors.Is(err, ErrTaken) {
		t.Errorf("bob took alice's handle: %v", err)
	}
	// alice keeps it when she saves again, and releases it when she changes it.
	if _, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "zed", Public: true}); err != nil {
		t.Errorf("alice saving her own handle again: %v", err)
	}
	if _, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "alice-z", Public: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.SaveProfile(r.ctx, "bob", profile.Profile{Handle: "zed", Public: true}); err != nil {
		t.Errorf("bob could not take the handle alice released: %v", err)
	}
	if _, err := r.e.Show(r.ctx, "zed", ""); err != nil {
		t.Errorf("zed: %v", err)
	}
}

func TestAProfileIsChecked(t *testing.T) {
	r := newRig(t, nil)
	for name, in := range map[string]profile.Profile{
		"public without a handle": {Public: true},
		"short handle":            {Handle: "ab"},
		"bad handle":              {Handle: "no spaces"},
		"http link":               {Handle: "alice", Links: profile.Links{GitHub: "http://github.com/alice"}},
		"javascript link":         {Handle: "alice", Links: profile.Links{Website: "javascript:alert(1)"}},
		"login in link":           {Handle: "alice", Links: profile.Links{X: "https://user:pw@x.com/a"}},
		"no host":                 {Handle: "alice", Links: profile.Links{X: "https:///a"}},
	} {
		if _, err := r.e.SaveProfile(r.ctx, "alice", in); !refuse.Is(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "alice", Links: profile.Links{GitHub: "https://github.com/alice", X: "https://x.com/alice", Website: "https://alice.dev"}})
	if err != nil || got.Links.Website != "https://alice.dev" {
		t.Errorf("good links: %v %+v", err, got)
	}
}

func TestThePublicProfileShowsStandingAndNothingPrivate(t *testing.T) {
	use := rule.Rule{ID: "u", Name: "u", Active: true, When: rule.When{Event: "product.used"}, Then: []rule.Action{pts(60, true), badge("b1")}}
	r := newRig(t, func(p *rule.Program) { p.Badges[0].Rarity = rule.Rare }, star("s", pts(50, true), badge("b2")), use)
	for _, ev := range []catalog.Event{
		r.starEvent("e1", "alice", "x"),
		{ID: "e2", Type: "product.used", Subject: "alice", Email: "alice@example.com", Org: "acme", Fields: map[string]any{"product": "dev"}},
	} {
		for _, d := range r.eval(ev).Decisions {
			if _, err := r.e.Approve(r.ctx, d.Claim.Id(), "staff"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := r.e.Backfill(r.ctx, r.slug, []ledger.Past{{Subject: "alice", Source: "usage.tokens", Points: 7}}, "z"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.SaveProfile(r.ctx, "alice", profile.Profile{Handle: "alice", Public: true, Links: profile.Links{GitHub: "https://github.com/alice"}}); err != nil {
		t.Fatal(err)
	}
	pub, err := r.e.Show(r.ctx, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if pub.Handle != "alice" || pub.Points != 117 || pub.Level != 2 || pub.LevelName != "Builder" {
		t.Errorf("standing = %+v", pub)
	}
	if pub.Pillars["community"] != 1 || pub.Pillars["use"] != 1 || pub.Pillars["expert"] != 0 || len(pub.Pillars) != 5 {
		t.Errorf("pillars = %+v", pub.Pillars)
	}
	if len(pub.Badges) != 2 {
		t.Fatalf("badges = %+v", pub.Badges)
	}
	top := pub.Top(3)
	if top[0].ID != "b1" || top[0].Rarity != rule.Rare || top[1].Rarity != rule.Common || top[0].EarnedAt == "" {
		t.Errorf("top = %+v: the rarer badge goes first and earned_at is set", top)
	}
	if pub.Links.GitHub != "https://github.com/alice" {
		t.Errorf("links = %+v", pub.Links)
	}
	// nothing private: no email, no org, no subject, no redeemable balance.
	raw := marshal(pub)
	for _, secret := range []string{"alice@example.com", "acme", "redeemable"} {
		if contains(raw, secret) {
			t.Errorf("the public profile carries %q: %s", secret, raw)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestExploreListsWhatIsLeftByPillarInOrder(t *testing.T) {
	follow := rule.Rule{ID: "f", Name: "Follow", Active: true, When: rule.When{Event: "github.follow"}, Then: []rule.Action{pts(1, false), badge("b2")}}
	off := rule.Rule{ID: "off", Name: "Off", Active: false, When: rule.When{Event: "model.used"}, Then: []rule.Action{pts(0, false)}}
	used := rule.Rule{ID: "u", Name: "Used", Active: true, When: rule.When{Event: "product.used"}, Then: []rule.Action{pts(5, false)}}
	r := newRig(t, nil, star("s", pts(1, false), badge("b1")), follow, off, used)

	groups, err := r.e.Explore(r.ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var order []catalog.Pillar
	for _, g := range groups {
		order = append(order, g.ID)
	}
	want := []catalog.Pillar{catalog.Use, catalog.Share, catalog.Community, catalog.Contribute, catalog.Expert}
	if len(order) != 5 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] || order[3] != want[3] || order[4] != want[4] {
		t.Fatalf("pillars = %v", order)
	}
	if len(groups[0].Rules) != 1 || groups[0].Rules[0].Rule != "u" || groups[0].Rules[0].Points != 5 {
		t.Errorf("use = %+v", groups[0].Rules)
	}
	if len(groups[2].Rules) != 2 {
		t.Fatalf("community = %+v", groups[2].Rules)
	}
	s := groups[2].Rules[0]
	if s.Rule != "s" || s.Points != 1 || len(s.Badges) != 1 || s.Badges[0].ID != "b1" || s.Event.Name != "github.star" || s.Event.Title == "" {
		t.Errorf("star prospect = %+v", s)
	}
	for _, g := range groups {
		for _, p := range g.Rules {
			if p.Rule == "off" {
				t.Error("an inactive rule is on the list")
			}
		}
	}

	// a rule the subject has a claim under is done; another subject still sees it.
	r.eval(r.starEvent("e1", "alice", "x"))
	groups, _ = r.e.Explore(r.ctx, "alice")
	if len(groups[2].Rules) != 1 || groups[2].Rules[0].Rule != "f" {
		t.Errorf("after starring: %+v", groups[2].Rules)
	}
	groups, _ = r.e.Explore(r.ctx, "bob")
	if len(groups[2].Rules) != 2 {
		t.Errorf("bob's list: %+v", groups[2].Rules)
	}
	// a rejected claim does not count as done.
	c := r.claims("alice")[0]
	if _, err := r.e.Reject(r.ctx, c.Id(), "staff", "no"); err != nil {
		t.Fatal(err)
	}
	groups, _ = r.e.Explore(r.ctx, "alice")
	if len(groups[2].Rules) != 2 {
		t.Errorf("after a rejection: %+v", groups[2].Rules)
	}
}
