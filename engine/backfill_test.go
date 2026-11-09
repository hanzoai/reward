package engine

import (
	"testing"

	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

func TestBackfillIsIdempotentPerSubjectAndSource(t *testing.T) {
	r := newRig(t, nil)
	rows := []ledger.Past{
		{Subject: "alice", Source: "usage.tokens", Points: 40, Evidence: "4M tokens"},
		{Subject: "alice", Source: "github.star", Points: 3, Evidence: "3 stars"},
		{Subject: "bob", Source: "referral.paying", Points: 20, Evidence: "1 org with spend"},
	}
	got, err := r.e.Backfill(r.ctx, r.slug, rows, "z@hanzo.ai")
	if err != nil || got.Written != 3 || got.Skipped != 0 || got.Points != 63 {
		t.Fatalf("first: %v %+v", err, got)
	}
	if a := r.account("alice"); a.Lifetime != 43 {
		t.Errorf("alice = %+v", a)
	}

	// the same rows again change nothing.
	got, err = r.e.Backfill(r.ctx, r.slug, rows, "z@hanzo.ai")
	if err != nil || got.Written != 0 || got.Skipped != 3 || got.Points != 0 {
		t.Fatalf("second: %v %+v", err, got)
	}
	// so does the same (subject, source) with another count: the first stands.
	got, err = r.e.Backfill(r.ctx, r.slug, []ledger.Past{{Subject: "alice", Source: "usage.tokens", Points: 999, Evidence: "later"}}, "z@hanzo.ai")
	if err != nil || got.Written != 0 || got.Skipped != 1 {
		t.Fatalf("recount: %v %+v", err, got)
	}
	if a := r.account("alice"); a.Lifetime != 43 || len(r.rows("alice")) != 2 {
		t.Errorf("alice after reruns = %+v over %d rows", a, len(r.rows("alice")))
	}
	// a new source for the same subject is new.
	got, _ = r.e.Backfill(r.ctx, r.slug, []ledger.Past{{Subject: "alice", Source: "github.fork", Points: 2, Evidence: "2 forks"}}, "z@hanzo.ai")
	if got.Written != 1 || r.account("alice").Lifetime != 45 {
		t.Errorf("fork: %+v", got)
	}
}

func TestBackfillIsOneAuditedActNamingWhoRanIt(t *testing.T) {
	r := newRig(t, nil)
	rows := []ledger.Past{
		{Subject: "alice", Source: "github.star", Points: 3},
		{Subject: "bob", Source: "github.star", Points: 5},
		{Subject: "carol", Source: "bounty", Points: 75},
	}
	if _, err := r.e.Backfill(r.ctx, r.slug, rows, "z@hanzo.ai"); err != nil {
		t.Fatal(err)
	}
	acts, err := ledger.Acts(r.ctx, r.e.DB, "ledger.backfilled", "program/"+r.slug)
	if err != nil || len(acts) != 1 {
		t.Fatalf("trail rows = %d (%v), want one for the whole call", len(acts), err)
	}
	if acts[0].Actor != "z@hanzo.ai" || acts[0].Detail["written"] != float64(3) || acts[0].Detail["points"] != float64(83) {
		t.Errorf("trail = %+v", acts[0])
	}
	// no claims: it is not thousands of manual approvals.
	if n := len(r.claims("alice")); n != 0 {
		t.Errorf("a backfill made %d claims", n)
	}
	for _, x := range r.rows("carol") {
		if x.By != "z@hanzo.ai" || x.Act != ledger.Legacy || x.Source != "bounty" {
			t.Errorf("row = %+v", x)
		}
	}
	if _, err := r.e.Backfill(r.ctx, r.slug, rows, " "); !refuse.Is(err) {
		t.Errorf("a backfill nobody ran: %v", err)
	}
}

func TestLegacyPointsAreNeverRedeemable(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	// a legacy row claiming to be redeemable is not: no row of a backfill can set it.
	if _, err := r.e.Backfill(r.ctx, r.slug, []ledger.Past{{Subject: "alice", Source: "usage.tokens", Points: 500}}, "z"); err != nil {
		t.Fatal(err)
	}
	for _, x := range r.rows("alice") {
		if x.Redeemable {
			t.Errorf("legacy row is redeemable: %+v", x)
		}
	}
	if a := r.account("alice"); a.Lifetime != 500 || a.Redeemable != 0 {
		t.Fatalf("legacy: %+v", a)
	}
	// even a row tampered into the store as redeemable is not counted as such.
	x := r.rows("alice")[0]
	x.Redeemable = true
	if err := x.PutCtx(r.ctx); err != nil {
		t.Fatal(err)
	}
	if a := r.account("alice"); a.Redeemable != 0 {
		t.Errorf("a flagged legacy row counted as redeemable: %+v", a)
	}
	// an earned, redeemable award beside it counts as it should.
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
		t.Fatal(err)
	}
	if a := r.account("alice"); a.Lifetime != 510 || a.Redeemable != 10 {
		t.Errorf("legacy and earned: %+v", a)
	}
}

func TestLevelsAreComputedIncludingLegacy(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	if a := r.account("alice"); a.Level.Name != "Hacker" || a.Level.Level != 1 || a.Band != "Hacker" {
		t.Fatalf("start: %+v", a)
	}
	if _, err := r.e.Backfill(r.ctx, r.slug, []ledger.Past{{Subject: "alice", Source: "usage.tokens", Points: 95}}, "z"); err != nil {
		t.Fatal(err)
	}
	a := r.account("alice")
	if a.Level.Name != "Hacker" || a.Next == nil || a.Next.Name != "Builder" {
		t.Fatalf("95 legacy: %+v", a)
	}
	// ten earned points on top of 95 legacy cross the 100 line.
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
		t.Fatal(err)
	}
	if a := r.account("alice"); a.Lifetime != 105 || a.Level.Name != "Builder" {
		t.Errorf("105 with legacy: %+v", a)
	}
	// and legacy alone can carry a subject there.
	if _, err := r.e.Backfill(r.ctx, r.slug, []ledger.Past{{Subject: "bob", Source: "usage.tokens", Points: 600}}, "z"); err != nil {
		t.Fatal(err)
	}
	if a := r.account("bob"); a.Level.Name != "Shipper" {
		t.Errorf("600 legacy: %+v", a)
	}
}

func TestBackfillRefusesAWrongRowWholesale(t *testing.T) {
	r := newRig(t, nil)
	good := ledger.Past{Subject: "alice", Source: "github.star", Points: 1}
	for name, bad := range map[string]ledger.Past{
		"no subject":     {Source: "github.star", Points: 1},
		"unknown source": {Subject: "bob", Source: "vibes", Points: 1},
		"no points":      {Subject: "bob", Source: "github.star", Points: 0},
		"negative":       {Subject: "bob", Source: "github.star", Points: -5},
	} {
		if _, err := r.e.Backfill(r.ctx, r.slug, []ledger.Past{good, bad}, "z"); !refuse.Is(err) {
			t.Errorf("%s: %v", name, err)
		}
		if len(r.rows("alice")) != 0 {
			t.Errorf("%s: the good row was written beside a bad one", name)
		}
	}
	if _, err := r.e.Backfill(r.ctx, r.slug, nil, "z"); !refuse.Is(err) {
		t.Errorf("no rows: %v", err)
	}
	if _, err := r.e.Backfill(r.ctx, "nope", []ledger.Past{good}, "z"); err == nil {
		t.Error("a backfill into a program that does not exist")
	}
}

func TestEveryBackfillSourceIsNamed(t *testing.T) {
	for _, s := range []string{"usage.tokens", "referral.signup", "referral.paying", "github.star", "github.follow", "github.fork", "bounty"} {
		found := false
		for _, x := range ledger.Sources {
			found = found || x == s
		}
		if !found {
			t.Errorf("%s is not a legacy source", s)
		}
	}
	_ = rule.Common
}
