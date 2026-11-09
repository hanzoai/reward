package engine

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

// ── claims ──────────────────────────────────────────────────────────────────

func TestAMatchingEventMakesAPendingClaimAndAwardsNothing(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true), badge("b1")))
	d := r.decision(r.eval(r.starEvent("e1", "alice", "hanzoai/commerce")), "s")
	if d.Result != Claimed || d.Claim.Status != claim.Pending || d.Claim.Subject != "alice" {
		t.Fatalf("decision = %+v", d)
	}
	if d.Claim.Id() != claim.ID("e1", "s", 1) || d.Claim.RuleVersion != 1 {
		t.Errorf("claim id %s, version %d", d.Claim.Id(), d.Claim.RuleVersion)
	}
	if a := r.account("alice"); a.Lifetime != 0 || len(a.Badges) != 0 || len(r.rows("alice")) != 0 {
		t.Errorf("a pending claim wrote to the ledger: %+v", a)
	}
}

func TestNonMatchingAndInactiveRulesMakeNoClaim(t *testing.T) {
	off := star("off", pts(1, false))
	off.Active = false
	picky := star("picky", pts(1, false))
	picky.If = []rule.Condition{{Field: "repo", Op: rule.Eq, Value: "hanzoai/commerce"}}
	r := newRig(t, nil, off, picky)
	out := r.eval(r.starEvent("e1", "alice", "hanzoai/other"))
	if d := r.decision(out, "picky"); d.Result != Skipped || d.Reason != "if repo eq hanzoai/commerce" {
		t.Errorf("picky = %+v", d)
	}
	for _, d := range out.Decisions {
		if d.Rule == "off" {
			t.Error("an inactive rule was considered")
		}
	}
	if n := len(r.claims("alice")); n != 0 {
		t.Errorf("claims = %d, want 0", n)
	}
}

func TestAnInactiveProgramAwardsNothing(t *testing.T) {
	r := newRig(t, func(p *rule.Program) { p.Active = false }, star("s", pts(1, false)))
	if out := r.eval(r.starEvent("e1", "alice", "x")); len(out.Decisions) != 0 {
		t.Fatalf("an inactive program was evaluated: %+v", out.Decisions)
	}
}

func TestAnInvalidEventIsRefused(t *testing.T) {
	r := newRig(t, nil, star("s", pts(1, false)))
	_, err := r.e.Evaluate(r.ctx, catalog.Event{ID: "e1", Type: "github.star", Subject: "alice", Fields: map[string]any{}, Evidence: &catalog.Evidence{Source: "github", URL: "https://github.com/hanzoai/x", NeedsReview: true}})
	if !refuse.Is(err) {
		t.Errorf("err = %v, want a refusal", err)
	}
}

func TestTheSameEventTwiceIsOneClaim(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	ev := r.starEvent("e1", "alice", "x")
	first := r.decision(r.eval(ev), "s")
	second := r.decision(r.eval(ev), "s")
	if first.Result != Claimed || second.Result != Duplicate || second.Claim.Id() != first.Claim.Id() {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	if n := len(r.claims("alice")); n != 1 {
		t.Errorf("claims = %d, want 1", n)
	}
}

func TestTheSameEventAfterTheRuleChangedIsStillOneClaim(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	ev := r.starEvent("e1", "alice", "x")
	first := r.decision(r.eval(ev), "s")

	p, err := r.e.SaveRule(r.ctx, r.slug, star("s", pts(20, true)), "tester")
	if err != nil || p.Find("s").Version != 2 {
		t.Fatalf("save: %v %+v", err, p.Find("s"))
	}
	again := r.decision(r.eval(ev), "s")
	if again.Result != Duplicate || again.Claim.Id() != first.Claim.Id() {
		t.Fatalf("a redelivered event after an edit made a second claim: %+v", again)
	}
	if n := len(r.claims("alice")); n != 1 {
		t.Errorf("claims = %d, want 1", n)
	}
	// a different event is a new claim, under the new version.
	next := r.decision(r.eval(r.starEvent("e2", "alice", "y")), "s")
	if next.Result != Claimed || next.Claim.RuleVersion != 2 {
		t.Errorf("next = %+v", next)
	}
}

func TestConcurrentDeliveriesOfOneEventMakeOneClaim(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// each delivery is its own request, so its own event: Validate settles
			// the event in place and an Evidence pointer must not be shared.
			if _, err := r.e.Evaluate(r.ctx, r.starEvent("e1", "alice", "x")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(r.claims("alice")); n != 1 {
		t.Errorf("claims = %d, want 1", n)
	}
}

// ── limits ──────────────────────────────────────────────────────────────────

func TestLimitOnceSubject(t *testing.T) {
	s := star("s", pts(10, false))
	s.Limits = rule.Limits{Once: rule.Subject}
	r := newRig(t, nil, s)
	if d := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s"); d.Result != Claimed {
		t.Fatalf("first = %+v", d)
	}
	if d := r.decision(r.eval(r.starEvent("e2", "alice", "y")), "s"); d.Result != Skipped {
		t.Errorf("a second event for the same subject = %+v", d)
	}
	if d := r.decision(r.eval(r.starEvent("e3", "bob", "x")), "s"); d.Result != Claimed {
		t.Errorf("another subject = %+v", d)
	}
}

func TestLimitOnceSubjectAndKey(t *testing.T) {
	s := star("s", pts(10, false))
	s.Limits = rule.Limits{Once: rule.Keyed, KeyField: "repo"}
	r := newRig(t, nil, s)
	got := func(id, subject, repo string) string {
		return r.decision(r.eval(r.starEvent(id, subject, repo)), "s").Result
	}
	if got("e1", "alice", "a") != Claimed || got("e2", "alice", "b") != Claimed {
		t.Fatal("two repos should both be claimed")
	}
	if got("e3", "alice", "a") != Skipped {
		t.Error("the same repo twice was claimed")
	}
	if got("e4", "bob", "a") != Claimed {
		t.Error("another subject was held to alice's repo")
	}
}

func TestLimitPerDayAndPerMonth(t *testing.T) {
	s := star("s", pts(1, false))
	s.Limits = rule.Limits{PerDay: 2, PerMonth: 3}
	r := newRig(t, nil, s)
	got := func(id string) Decision { return r.decision(r.eval(r.starEvent(id, "alice", id)), "s") }
	if got("e1").Result != Claimed || got("e2").Result != Claimed {
		t.Fatal("the first two of the day should be claimed")
	}
	if d := got("e3"); d.Result != Skipped || d.Reason == "" {
		t.Errorf("third on the day = %+v", d)
	}
	r.now = r.now.Add(24 * time.Hour) // next UTC day, same month
	if got("e4").Result != Claimed {
		t.Error("a new day did not reset perDay")
	}
	if d := got("e5"); d.Result != Skipped {
		t.Errorf("the 4th of the month = %+v, want perMonth to refuse (perMonth 3)", d)
	}
	r.now = time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC)
	if got("e6").Result != Claimed {
		t.Error("a new month did not reset perMonth")
	}
}

func TestLimitsCountPerSubject(t *testing.T) {
	s := star("s", pts(1, false))
	s.Limits = rule.Limits{PerDay: 1}
	r := newRig(t, nil, s)
	if r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Result != Claimed ||
		r.decision(r.eval(r.starEvent("e2", "bob", "x")), "s").Result != Claimed {
		t.Error("alice's claim used up bob's day")
	}
}

func TestARejectedClaimFreesItsLimit(t *testing.T) {
	s := star("s", pts(1, false))
	s.Limits = rule.Limits{Once: rule.Subject}
	r := newRig(t, nil, s)
	d := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s")
	if _, err := r.e.Reject(r.ctx, d.Claim.Id(), "staff", "not real"); err != nil {
		t.Fatal(err)
	}
	if r.decision(r.eval(r.starEvent("e2", "alice", "x")), "s").Result != Claimed {
		t.Error("a rejected claim still held the subject's once")
	}
}

// ── budget ──────────────────────────────────────────────────────────────────

func TestBudgetStopsClaimsOncePointsAreSpent(t *testing.T) {
	r := newRig(t, func(p *rule.Program) { p.Budget = rule.Budget{PointsPerMonth: 25} }, star("s", pts(10, false)))
	got := func(id string) Decision { return r.decision(r.eval(r.starEvent(id, "u"+id, id)), "s") }
	if got("1").Result != Claimed || got("2").Result != Claimed {
		t.Fatal("20 of 25 points should fit")
	}
	if d := got("3"); d.Result != Skipped || d.Reason == "" {
		t.Errorf("the claim that would pass 25 = %+v", d)
	}
	r.now = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	if got("4").Result != Claimed {
		t.Error("a new month did not renew the budget")
	}
}

func TestBudgetCountsPendingClaimsAndFreesRejectedOnes(t *testing.T) {
	r := newRig(t, func(p *rule.Program) { p.Budget = rule.Budget{PointsPerMonth: 10} }, star("s", pts(10, false)))
	first := r.decision(r.eval(r.starEvent("e1", "alice", "a")), "s")
	if first.Result != Claimed {
		t.Fatal(first)
	}
	if r.decision(r.eval(r.starEvent("e2", "bob", "b")), "s").Result != Skipped {
		t.Fatal("a pending claim did not hold its points")
	}
	if _, err := r.e.Reject(r.ctx, first.Claim.Id(), "staff", "fake"); err != nil {
		t.Fatal(err)
	}
	if r.decision(r.eval(r.starEvent("e3", "bob", "c")), "s").Result != Claimed {
		t.Error("a rejected claim kept holding its points")
	}
}

func TestBudgetBoundsCreditCents(t *testing.T) {
	r := newRig(t, func(p *rule.Program) { p.Budget = rule.Budget{CreditCentsPerMonth: 1000} }, star("s", cash(400)))
	got := func(id string) Decision {
		ev := r.starEvent(id, "u"+id, id)
		ev.Org = "acme"
		return r.decision(r.eval(ev), "s")
	}
	if got("1").Result != Claimed || got("2").Result != Claimed {
		t.Fatal("800 of 1000 cents should fit")
	}
	if d := got("3"); d.Result != Skipped {
		t.Errorf("1200 cents = %+v", d)
	}
}

func TestAProgramWithNoBudgetIsNotBounded(t *testing.T) {
	r := newRig(t, nil, star("s", pts(1_000_000, false)))
	if r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Result != Claimed {
		t.Error("budget 0 must mean no bound")
	}
}

// ── approval ────────────────────────────────────────────────────────────────

func TestManualWaitsForStaffAndAutoDoesNot(t *testing.T) {
	manual := star("manual", pts(10, true))
	auto := star("auto", pts(5, false))
	auto.Approval = rule.Auto
	r := newRig(t, nil, manual, auto)
	out := r.eval(r.starEvent("e1", "alice", "x"))
	if d := r.decision(out, "manual"); d.Claim.Status != claim.Pending || d.Claim.Approval != rule.Manual {
		t.Errorf("manual = %+v", d.Claim)
	}
	d := r.decision(out, "auto")
	if d.Claim.Status != claim.Awarded || d.Claim.Reviewer != "auto" {
		t.Errorf("auto = %+v", d.Claim)
	}
	if a := r.account("alice"); a.Lifetime != 5 {
		t.Errorf("lifetime = %d, want only the auto rule's 5", a.Lifetime)
	}
	if _, err := r.e.Approve(r.ctx, r.decision(out, "manual").Claim.Id(), "staff"); err != nil {
		t.Fatal(err)
	}
	if a := r.account("alice"); a.Lifetime != 15 {
		t.Errorf("lifetime = %d, want 15", a.Lifetime)
	}
}

func TestApprovingTwiceWritesOnce(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true), badge("b1")))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	for range 3 {
		got, err := r.e.Approve(r.ctx, c.Id(), "staff")
		if err != nil || got.Status != claim.Awarded || got.Reviewer != "staff" || got.ReviewedAt.IsZero() {
			t.Fatalf("approve: %v %+v", err, got)
		}
	}
	if rows := r.rows("alice"); len(rows) != 2 {
		t.Errorf("ledger rows = %d, want 2 (points and badge)", len(rows))
	}
	if a := r.account("alice"); a.Lifetime != 10 || len(a.Badges) != 1 {
		t.Errorf("account = %+v", a)
	}
	if n := r.trail("claim.approved", "claim/"+c.Id()); n != 1 {
		t.Errorf("trail records = %d, want 1", n)
	}
}

func TestConcurrentApprovalsWriteOnce(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true), badge("b1")))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if rows := r.rows("alice"); len(rows) != 2 {
		t.Errorf("ledger rows = %d, want 2", len(rows))
	}
	if n := r.trail("claim.approved", "claim/"+c.Id()); n != 1 {
		t.Errorf("trail records = %d, want 1", n)
	}
}

func TestApproveAndRejectRacingHaveOneWinner(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	var wg sync.WaitGroup
	var approved, rejected, conflicts int
	var mu sync.Mutex
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = r.e.Approve(r.ctx, c.Id(), "a")
			} else {
				_, err = r.e.Reject(r.ctx, c.Id(), "b", "no")
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrState):
				conflicts++
			case err != nil:
				t.Error(err)
			case i%2 == 0:
				approved++
			default:
				rejected++
			}
		}()
	}
	wg.Wait()
	if (approved > 0) == (rejected > 0) {
		t.Fatalf("approved %d, rejected %d: exactly one verdict may stand", approved, rejected)
	}
	got, _ := r.e.Claim(r.ctx, c.Id())
	switch got.Status {
	case claim.Awarded:
		if r.account("alice").Lifetime != 10 {
			t.Error("approved but not written")
		}
	case claim.Rejected:
		if r.account("alice").Lifetime != 0 {
			t.Error("rejected but written")
		}
	default:
		t.Errorf("status = %s", got.Status)
	}
}

func TestStaffActsNeedAReviewerAndANote(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	if _, err := r.e.Approve(r.ctx, c.Id(), " "); !refuse.Is(err) {
		t.Errorf("approve with no reviewer: %v", err)
	}
	if _, err := r.e.Reject(r.ctx, c.Id(), "staff", ""); !refuse.Is(err) {
		t.Errorf("reject with no note: %v", err)
	}
	if _, err := r.e.Reverse(r.ctx, c.Id(), "", "why"); !refuse.Is(err) {
		t.Errorf("reverse with no reviewer: %v", err)
	}
	if _, err := r.e.Approve(r.ctx, "clm_nope", "staff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("approve of nothing: %v", err)
	}
}

func TestRejectThenReverse(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true), badge("b1")))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim

	rej, err := r.e.Reject(r.ctx, c.Id(), "staff", "star farm")
	if err != nil || rej.Status != claim.Rejected || rej.Note != "star farm" || rej.Reviewer != "staff" {
		t.Fatalf("reject: %v %+v", err, rej)
	}
	if rows := r.rows("alice"); len(rows) != 0 {
		t.Errorf("a rejected claim wrote %d rows", len(rows))
	}
	if _, err := r.e.Reverse(r.ctx, c.Id(), "staff", "undo"); !errors.Is(err, ErrState) {
		t.Errorf("reversing a rejected claim: %v, want ErrState", err)
	}
	if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); !errors.Is(err, ErrState) {
		t.Errorf("approving a rejected claim: %v, want ErrState", err)
	}
	if again, err := r.e.Reject(r.ctx, c.Id(), "staff", "star farm"); err != nil || again.Status != claim.Rejected {
		t.Errorf("rejecting twice: %v", err)
	}
	if n := r.trail("claim.rejected", "claim/"+c.Id()); n != 1 {
		t.Errorf("reject trail records = %d, want 1", n)
	}
}

func TestReverseUndoesWithCompensatingEntries(t *testing.T) {
	r := newRig(t, nil, star("s", pts(120, true), badge("b1")))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	if _, err := r.e.Reverse(r.ctx, c.Id(), "staff", "too early"); !errors.Is(err, ErrState) {
		t.Fatalf("reversing a pending claim: %v, want ErrState", err)
	}
	c = r.decision(r.eval(r.starEvent("e2", "alice", "y")), "s").Claim
	if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
		t.Fatal(err)
	}
	if a := r.account("alice"); a.Lifetime != 120 || a.Redeemable != 120 || len(a.Badges) != 1 || a.Level.Name != "Builder" {
		t.Fatalf("before: %+v", a)
	}
	rev, err := r.e.Reverse(r.ctx, c.Id(), "staff", "fraud")
	if err != nil || rev.Status != claim.Reversed || rev.Note != "fraud" {
		t.Fatalf("reverse: %v %+v", err, rev)
	}
	if again, err := r.e.Reverse(r.ctx, c.Id(), "staff", "fraud"); err != nil || again.Status != claim.Reversed {
		t.Fatalf("reversing twice: %v", err)
	}
	a := r.account("alice")
	if a.Lifetime != 0 || a.Redeemable != 0 || len(a.Badges) != 0 || a.Level.Name != "Hacker" {
		t.Errorf("after: %+v", a)
	}
	// the award rows stay; the reversal is more rows, never an edit.
	rows := r.rows("alice")
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 2 awards and 2 reversals", len(rows))
	}
	var awards, reversals int
	for _, x := range rows {
		switch x.Act {
		case "award":
			awards++
		case "reversal":
			reversals++
			if x.Reverses == "" || x.Claim != c.Id() {
				t.Errorf("reversal %+v does not name what it undoes", x)
			}
		}
	}
	if awards != 2 || reversals != 2 {
		t.Errorf("awards %d reversals %d", awards, reversals)
	}
	if n := r.trail("claim.reversed", "claim/"+c.Id()); n != 1 {
		t.Errorf("reverse trail records = %d, want 1", n)
	}
}

func TestABadgeTwoClaimsAwardedSurvivesOneReversal(t *testing.T) {
	r := newRig(t, nil, star("s", badge("b1")))
	one := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	two := r.decision(r.eval(r.starEvent("e2", "alice", "y")), "s").Claim
	for _, c := range []*claim.Claim{one, two} {
		if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.e.Reverse(r.ctx, one.Id(), "staff", "dup"); err != nil {
		t.Fatal(err)
	}
	if a := r.account("alice"); len(a.Badges) != 1 {
		t.Errorf("badges = %d, want the one the other claim still awards", len(a.Badges))
	}
}

func TestAListedStatusIsHealedToTheStepsWhenTheClaimIsRead(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	// a decision written, its status never settled: the writer died between the two.
	if _, err := r.e.step(r.ctx, c.Id(), claim.Decision, claim.Reject, "staff", "no"); err != nil {
		t.Fatal(err)
	}
	if got := r.claims("alice")[0].Status; got != claim.Pending {
		t.Fatalf("the column moved by itself: %s", got)
	}
	got, err := r.e.Claim(r.ctx, c.Id())
	if err != nil || got.Status != claim.Rejected {
		t.Fatalf("read: %v %+v", err, got)
	}
	if got := r.claims("alice")[0].Status; got != claim.Rejected {
		t.Errorf("the listed status = %s, want it healed to rejected", got)
	}
}

// ── balances and levels ─────────────────────────────────────────────────────

func TestLifetimeAndRedeemableAreSeparateFolds(t *testing.T) {
	follow := rule.Rule{ID: "pr", Name: "pr", Active: true, When: rule.When{Event: "github.follow"}, Then: []rule.Action{pts(70, true)}}
	r := newRig(t, nil, star("rep", pts(30, false)), follow)
	a := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "rep").Claim
	f := r.decision(r.eval(catalog.Event{ID: "e2", Type: "github.follow", Subject: "alice", Fields: map[string]any{"target": "hanzoai"}, Evidence: &catalog.Evidence{Source: "github", URL: "https://github.com/hanzoai/x", NeedsReview: true}}), "pr").Claim
	for _, c := range []*claim.Claim{a, f} {
		if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
			t.Fatal(err)
		}
	}
	acct := r.account("alice")
	if acct.Lifetime != 100 || acct.Redeemable != 70 {
		t.Fatalf("lifetime %d redeemable %d, want 100 and 70", acct.Lifetime, acct.Redeemable)
	}
	// reversing a redeemable award lowers both; the plain one's points stay.
	if _, err := r.e.Reverse(r.ctx, f.Id(), "staff", "x"); err != nil {
		t.Fatal(err)
	}
	if acct := r.account("alice"); acct.Lifetime != 30 || acct.Redeemable != 0 {
		t.Errorf("after reversing the redeemable award: %+v", acct)
	}
}

func TestLevelFollowsLifetimePoints(t *testing.T) {
	p := &rule.Program{Levels: []rule.Level{{Level: 1, Name: "Hacker"}, {Level: 2, Name: "Builder", MinLifetimePoints: 100}, {Level: 3, Name: "Shipper", MinLifetimePoints: 500}}}
	cases := []struct {
		points int64
		level  string
		next   int64
	}{{0, "Hacker", 100}, {99, "Hacker", 100}, {100, "Builder", 500}, {499, "Builder", 500}, {500, "Shipper", 0}, {10_000, "Shipper", 0}}
	for _, c := range cases {
		l, next := p.LevelOf(c.points)
		if l.Name != c.level || l.Band != c.level {
			t.Errorf("%d points: level %s in band %s, want %s in the band of its own name", c.points, l.Name, l.Band, c.level)
		}
		if (next == nil) != (c.next == 0) || (next != nil && next.MinLifetimePoints != c.next) {
			t.Errorf("%d points: next %+v, want %d", c.points, next, c.next)
		}
	}
}

// ── the rule version a claim was made under ─────────────────────────────────

func TestAClaimAwardsWhatItsRuleVersionSaid(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	c := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s").Claim
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("s", pts(999, true)), "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
		t.Fatal(err)
	}
	if a := r.account("alice"); a.Lifetime != 10 {
		t.Errorf("lifetime = %d, want the 10 version 1 said", a.Lifetime)
	}
	if rows := r.rows("alice"); len(rows) != 1 || rows[0].RuleVersion != 1 {
		t.Errorf("rows = %+v, want one under rule version 1", rows)
	}
}

func TestRuleVersionsRiseOnlyWhenTheRuleChanges(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true)))
	version := func() int { p, _ := r.e.Program(r.ctx, r.slug); return p.Find("s").Version }
	if version() != 1 {
		t.Fatalf("version = %d", version())
	}
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("s", pts(10, true)), "tester"); err != nil || version() != 1 {
		t.Errorf("saving the same rule raised the version to %d (%v)", version(), err)
	}
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("s", pts(11, true)), "tester"); err != nil || version() != 2 {
		t.Errorf("changing points: version %d (%v)", version(), err)
	}
	off := star("s", pts(11, true))
	off.Active = false
	if _, err := r.e.SaveRule(r.ctx, r.slug, off, "tester"); err != nil || version() != 3 {
		t.Errorf("deactivating: version %d (%v)", version(), err)
	}
}

// ── credit and email ────────────────────────────────────────────────────────

func TestCreditGoesThroughCreditsOncePerClaim(t *testing.T) {
	c := star("s", cash(500))
	c.Approval = rule.Auto
	r := newRig(t, nil, c)
	ev := r.starEvent("e1", "alice", "x")
	ev.Org = "acme"
	got := r.decision(r.eval(ev), "s").Claim
	if got.Status != claim.Awarded {
		t.Fatalf("claim = %+v", got)
	}
	r.eval(ev)
	if _, err := r.e.Approve(r.ctx, got.Id(), "staff"); err != nil {
		t.Fatal(err)
	}
	if n := r.bank.total("acme"); n != 500 || len(r.bank.grants) != 1 {
		t.Errorf("credit = %d cents over %d grants, want 500 over 1", n, len(r.bank.grants))
	}
	for key, g := range r.bank.grants {
		if key != fmt.Sprintf("reward:%s:0", got.Id()) || g.Actor != "auto" {
			t.Errorf("grant %q = %+v", key, g)
		}
	}
}

func TestACreditThatFailsLeavesTheClaimApprovedAndARetryFinishesIt(t *testing.T) {
	r := newRig(t, nil, star("s", pts(10, true), cash(300)))
	ev := r.starEvent("e1", "alice", "x")
	ev.Org = "acme"
	c := r.decision(r.eval(ev), "s").Claim

	r.bank.broken = true
	if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err == nil {
		t.Fatal("approve hid a failed credit")
	}
	cur, _ := r.e.Claim(r.ctx, c.Id())
	if cur.Status != claim.Approved {
		t.Fatalf("status = %s, want approved", cur.Status)
	}
	if _, err := r.e.Reject(r.ctx, c.Id(), "staff", "no"); !errors.Is(err, ErrState) {
		t.Errorf("a decided claim was rejected: %v", err)
	}
	r.bank.broken = false
	got, err := r.e.Approve(r.ctx, c.Id(), "someone else")
	if err != nil || got.Status != claim.Awarded || got.Reviewer != "staff" {
		t.Fatalf("retry: %v %+v", err, got)
	}
	if r.bank.total("acme") != 300 || len(r.bank.grants) != 1 {
		t.Errorf("credit = %d over %d grants", r.bank.total("acme"), len(r.bank.grants))
	}
	if a := r.account("alice"); a.Lifetime != 10 || len(r.rows("alice")) != 1 {
		t.Errorf("the points were written %d times: %+v", len(r.rows("alice")), a)
	}
}

func TestACreditRuleNeedsAnOrgAndAHostThatGrants(t *testing.T) {
	r := newRig(t, nil, star("s", cash(300)))
	if d := r.decision(r.eval(r.starEvent("e1", "alice", "x")), "s"); d.Result != Skipped || d.Reason == "" {
		t.Errorf("no org = %+v", d)
	}
	r.e.Credits = nil
	ev := r.starEvent("e2", "alice", "y")
	ev.Org = "acme"
	if d := r.decision(r.eval(ev), "s"); d.Result != Skipped {
		t.Errorf("no credit host = %+v", d)
	}
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("c", cash(1)), "tester"); !refuse.Is(err) {
		t.Errorf("a host that grants no credit saved a credit rule: %v", err)
	}
}

func TestACreditAboveTheCeilingIsRefusedWhenTheRuleIsSaved(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("c", cash(r.bank.ceiling+1)), "tester"); !refuse.Is(err) {
		t.Errorf("err = %v, want a refusal", err)
	}
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("c", cash(r.bank.ceiling)), "tester"); err != nil {
		t.Errorf("at the ceiling: %v", err)
	}
}

func TestAClaimThatGrantedCreditIsNotReversedHere(t *testing.T) {
	c := star("s", pts(10, true), cash(100))
	c.Approval = rule.Auto
	r := newRig(t, nil, c)
	ev := r.starEvent("e1", "alice", "x")
	ev.Org = "acme"
	got := r.decision(r.eval(ev), "s").Claim
	if _, err := r.e.Reverse(r.ctx, got.Id(), "staff", "oops"); !refuse.Is(err) {
		t.Errorf("err = %v, want a refusal", err)
	}
	if a := r.account("alice"); a.Lifetime != 10 {
		t.Errorf("a refused reversal still moved the ledger: %+v", a)
	}
}

func TestAnEmailActionSendsTheTemplateOnceWhenAwarded(t *testing.T) {
	s := star("s", pts(7, false), rule.Action{Type: rule.Do.Email, Template: "hello"})
	r := newRig(t, nil, s)
	if d := r.decision(r.eval(r.starEvent("e0", "alice", "x")), "s"); d.Result != Skipped {
		t.Fatalf("an email rule with no address = %+v", d)
	}
	ev := r.starEvent("e1", "alice", "y")
	ev.Email = "alice@example.com"
	c := r.decision(r.eval(ev), "s").Claim
	if len(r.mail.sent) != 0 {
		t.Fatal("mail was sent before the claim was approved")
	}
	for range 2 {
		if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.mail.sent) != 1 {
		t.Fatalf("sent %d, want 1", len(r.mail.sent))
	}
	want := [3]string{"alice@example.com", "Hi alice", "You earned 7 points in Test for s."}
	if r.mail.sent[0] != want {
		t.Errorf("sent %q, want %q", r.mail.sent[0], want)
	}
}

// ── simulate ────────────────────────────────────────────────────────────────

func TestSimulateAnswersAndWritesNothing(t *testing.T) {
	r := newRig(t, nil)
	draft := star("draft", pts(10, true))
	draft.Active = false // a draft is simulated whether or not it is on
	out, err := r.e.Simulate(r.ctx, r.slug, draft, r.starEvent("e1", "alice", "x"))
	if err != nil || !out.Dry || len(out.Decisions) != 1 || out.Decisions[0].Result != Claimed {
		t.Fatalf("simulate: %v %+v", err, out)
	}
	if got := out.Decisions[0].Claim; got.Points != 10 || got.Status != claim.Pending {
		t.Errorf("would-be claim = %+v", got)
	}
	if n := len(r.claims("alice")); n != 0 {
		t.Errorf("a dry run wrote %d claims", n)
	}
	// it applies limits as a real run would.
	once := star("once", pts(1, false))
	once.Limits = rule.Limits{Once: rule.Subject}
	if _, err := r.e.SaveRule(r.ctx, r.slug, once, "tester"); err != nil {
		t.Fatal(err)
	}
	r.eval(r.starEvent("e2", "alice", "x"))
	out, _ = r.e.Simulate(r.ctx, r.slug, once, r.starEvent("e3", "alice", "y"))
	if out.Decisions[0].Result != Skipped {
		t.Errorf("simulating past a limit = %+v", out.Decisions[0])
	}
	// a draft that is not a valid rule is refused, not simulated.
	if _, err := r.e.Simulate(r.ctx, r.slug, star("bad", badge("zz")), r.starEvent("e4", "alice", "x")); !refuse.Is(err) {
		t.Errorf("err = %v, want a refusal", err)
	}
}

// ── configuration ───────────────────────────────────────────────────────────

func TestProgramHeaderIsCheckedAndRulesAreKept(t *testing.T) {
	r := newRig(t, nil, star("s", pts(1, false)))
	cur, _ := r.e.Program(r.ctx, r.slug)
	for name, mod := range map[string]func(*rule.Program){
		"negative budget":     func(p *rule.Program) { p.Budget.PointsPerMonth = -1 },
		"levels not from 0":   func(p *rule.Program) { p.Levels[0].MinLifetimePoints = 5 },
		"levels out of order": func(p *rule.Program) { p.Levels[2].MinLifetimePoints = 50 },
		"level numbering":     func(p *rule.Program) { p.Levels[1].Level = 7 },
		"badge twice":         func(p *rule.Program) { p.Badges = append(p.Badges, p.Badges[0]) },
		"badge pillar":        func(p *rule.Program) { p.Badges[0].Pillar = "fun" },
		"bad template":        func(p *rule.Program) { p.Templates[0].Body = "{{.x" },
		"no name":             func(p *rule.Program) { p.Name = "" },
	} {
		var in rule.Program
		in.Name, in.Active, in.Budget = cur.Name, cur.Active, cur.Budget
		in.Levels, in.Badges, in.Templates = slices.Clone(cur.Levels), slices.Clone(cur.Badges), slices.Clone(cur.Templates)
		mod(&in)
		if _, err := r.e.SaveProgram(r.ctx, r.slug, in, "tester"); !refuse.Is(err) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	// dropping a badge a rule awards is refused; the rule would be left broken.
	r2 := newRig(t, nil, star("s", badge("b1")))
	cur2, _ := r2.e.Program(r2.ctx, r2.slug)
	var in rule.Program
	in.Name, in.Active, in.Levels, in.Badges = cur2.Name, cur2.Active, cur2.Levels, []rule.Badge{{ID: "b2", Name: "Two", Pillar: catalog.Contribute}}
	if _, err := r2.e.SaveProgram(r2.ctx, r2.slug, in, "tester"); !refuse.Is(err) {
		t.Errorf("dropping a used badge: %v", err)
	}
	// a header save leaves the rules as stored, whatever it carries.
	in = rule.Program{}
	in.Name, in.Active, in.Levels, in.Badges, in.Templates = "Renamed", cur.Active, cur.Levels, cur.Badges, cur.Templates
	got, err := r.e.SaveProgram(r.ctx, r.slug, in, "tester")
	if err != nil || got.Name != "Renamed" || len(got.Rules) != 1 || got.Version != 2 {
		t.Errorf("rename: %v %+v", err, got)
	}
	if again, err := r.e.SaveProgram(r.ctx, r.slug, in, "tester"); err != nil || again.Version != 2 {
		t.Errorf("saving the same header raised the version: %v %+v", err, again)
	}
}

func TestTheFoundingCutoffIsSavedWithTheProgramAndMustParse(t *testing.T) {
	r := newRig(t, func(p *rule.Program) { p.FoundingCutoff = "2026-01-01" })
	cur, err := r.e.Program(r.ctx, r.slug)
	if err != nil || cur.FoundingCutoff != "2026-01-01" || cur.View().FoundingCutoff != "2026-01-01" {
		t.Fatalf("created with a cutoff: %v %+v", err, cur)
	}
	if at, err := cur.Cutoff(); err != nil || !at.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("cutoff = %v %v", at, err)
	}
	header := func(cutoff string) rule.Program {
		return rule.Program{Name: cur.Name, Active: cur.Active, Levels: cur.Levels, Badges: cur.Badges, Templates: cur.Templates, FoundingCutoff: cutoff}
	}
	// a change is a save: a new version and a trail row; the same value again is neither.
	got, err := r.e.SaveProgram(r.ctx, r.slug, header(" 2026-06-30T12:00:00Z "), "tester")
	if err != nil || got.FoundingCutoff != "2026-06-30T12:00:00Z" || got.Version != 2 {
		t.Fatalf("a timestamp: %v %+v", err, got)
	}
	if again, err := r.e.SaveProgram(r.ctx, r.slug, header("2026-06-30T12:00:00Z"), "tester"); err != nil || again.Version != 2 {
		t.Errorf("the same cutoff raised the version: %v %+v", err, again)
	}
	back, _ := r.e.Program(r.ctx, r.slug)
	if back.FoundingCutoff != "2026-06-30T12:00:00Z" {
		t.Errorf("stored = %q", back.FoundingCutoff)
	}
	// an unparseable date is refused and leaves the stored one alone.
	for _, bad := range []string{"soon", "2026-02-30", "06/30/2026", "2026"} {
		if _, err := r.e.SaveProgram(r.ctx, r.slug, header(bad), "tester"); !refuse.Is(err) {
			t.Errorf("%q: err = %v, want a refusal", bad, err)
		}
	}
	if back, _ = r.e.Program(r.ctx, r.slug); back.FoundingCutoff != "2026-06-30T12:00:00Z" || back.Version != 2 {
		t.Errorf("a refused save changed the program: %+v", back)
	}
	// empty is no cutoff.
	cleared, err := r.e.SaveProgram(r.ctx, r.slug, header(""), "tester")
	if err != nil || cleared.FoundingCutoff != "" || cleared.Version != 3 {
		t.Errorf("empty: %v %+v", err, cleared)
	}
	if at, err := cleared.Cutoff(); err != nil || !at.IsZero() {
		t.Errorf("no cutoff = %v %v", at, err)
	}
	// a program created with a bad one is refused too.
	if _, err := r.e.SaveProgram(r.ctx, "other", rule.Program{Name: "Other", FoundingCutoff: "never"}, "tester"); !refuse.Is(err) {
		t.Errorf("creating with a bad cutoff: %v", err)
	}
}

func TestDeleteIsRefusedOnceAnythingHasClaimedUnderIt(t *testing.T) {
	spare := rule.Rule{ID: "spare", Name: "spare", Active: true, When: rule.When{Event: "github.follow"}, Then: []rule.Action{pts(1, false)}}
	r := newRig(t, nil, star("used", pts(1, false)), spare)
	r.eval(r.starEvent("e1", "alice", "x"))
	if _, err := r.e.DeleteRule(r.ctx, r.slug, "used", "tester"); !refuse.Is(err) {
		t.Errorf("deleting a claimed rule: %v", err)
	}
	if p, err := r.e.DeleteRule(r.ctx, r.slug, "spare", "tester"); err != nil || p.Find("spare") != nil {
		t.Errorf("deleting an unclaimed rule: %v", err)
	}
	if _, err := r.e.DeleteRule(r.ctx, r.slug, "spare", "tester"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a rule twice: %v", err)
	}
	if err := r.e.DeleteProgram(r.ctx, r.slug, "tester"); !refuse.Is(err) {
		t.Errorf("deleting a claimed program: %v", err)
	}
	empty := newRig(t, nil)
	if err := empty.e.DeleteProgram(empty.ctx, empty.slug, "tester"); err != nil {
		t.Errorf("deleting an empty program: %v", err)
	}
	if _, err := empty.e.Program(empty.ctx, empty.slug); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestConfigurationChangesAreOnTheTrail(t *testing.T) {
	r := newRig(t, nil)
	if n := r.trail("program.created", "program/"+r.slug); n != 1 {
		t.Errorf("created records = %d", n)
	}
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("s", pts(1, false)), "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("s", pts(2, false)), "tester"); err != nil {
		t.Fatal(err)
	}
	if n := r.trail("rule.created", "program/"+r.slug); n != 1 {
		t.Errorf("rule.created = %d", n)
	}
	if n := r.trail("rule.updated", "program/"+r.slug); n != 1 {
		t.Errorf("rule.updated = %d", n)
	}
	if _, err := r.e.SaveRule(r.ctx, r.slug, star("s", pts(3, false)), " "); !refuse.Is(err) {
		t.Errorf("a change with nobody behind it: %v", err)
	}
}

func TestClaimsListByStatusNewestFirst(t *testing.T) {
	r := newRig(t, nil, star("s", pts(1, false)))
	for i, repo := range []string{"a", "b", "c"} {
		r.now = r.now.Add(time.Minute)
		r.eval(r.starEvent(fmt.Sprint("e", i), "alice", repo))
	}
	all, err := r.e.Claims(r.ctx, Filter{Status: claim.Pending})
	if err != nil || len(all) != 3 || all[0].EventID != "e2" || all[2].EventID != "e0" {
		t.Fatalf("pending = %v %v", len(all), err)
	}
	if _, err := r.e.Approve(r.ctx, all[0].Id(), "staff"); err != nil {
		t.Fatal(err)
	}
	pending, _ := r.e.Claims(r.ctx, Filter{Status: claim.Pending})
	awarded, _ := r.e.Claims(r.ctx, Filter{Status: claim.Awarded})
	if len(pending) != 2 || len(awarded) != 1 {
		t.Errorf("pending %d awarded %d", len(pending), len(awarded))
	}
	page, _ := r.e.Claims(r.ctx, Filter{Limit: 1, Offset: 1})
	if len(page) != 1 {
		t.Errorf("page = %d", len(page))
	}
}

func TestAClaimCarriesTheEvidenceTheEventStated(t *testing.T) {
	r := newRig(t, nil, star("s", pts(1, false)))
	ev := r.starEvent("e1", "alice", "x")
	yes := true
	ev.Evidence = &catalog.Evidence{Source: "github", URL: "https://github.com/hanzoai/x",
		Checks: []catalog.Check{{FetchedAt: r.now, Status: 200, Public: true, RefFound: &yes, AuthorMatch: catalog.Unknown}}}
	d := r.decision(r.eval(ev), "s")
	got, err := r.e.Claim(r.ctx, d.Claim.Id())
	if err != nil || got.Evidence == nil || got.Evidence.URL != "https://github.com/hanzoai/x" || len(got.Evidence.Checks) != 1 || got.Evidence.Checks[0].AuthorMatch != catalog.Unknown {
		t.Fatalf("the claim read back %+v, %v", got.Evidence, err)
	}
	review := r.starEvent("e2", "alice", "y")
	review.Evidence = &catalog.Evidence{Source: "linkedin", URL: "https://www.linkedin.com/posts/x", NeedsReview: true}
	d = r.decision(r.eval(review), "s")
	got, _ = r.e.Claim(r.ctx, d.Claim.Id())
	if got.Evidence == nil || !got.Evidence.NeedsReview || len(got.Evidence.Checks) != 0 {
		t.Errorf("a source for review read back %+v", got.Evidence)
	}
}

func TestAWeekKeyAllowsOneAwardPerNetworkPerWeek(t *testing.T) {
	weekly := rule.Rule{ID: "w", Name: "w", Active: true, When: rule.When{Event: "social.post.verified"}, Then: []rule.Action{pts(5, false)},
		Limits: rule.Limits{Once: rule.Keyed, KeyField: "network+week"}}
	r := newRig(t, nil, weekly)
	post := func(id, network, week string) Decision {
		out := r.eval(catalog.Event{ID: id, Type: "social.post.verified", Subject: "alice",
			Fields:   map[string]any{"network": network, "url": "https://example.com/" + id, "product": "", "model": "", "week": week},
			Evidence: &catalog.Evidence{Source: network, URL: "https://example.com/" + id, NeedsReview: true}})
		return r.decision(out, "w")
	}
	if d := post("p1", "bluesky", "2026-W46"); d.Result != Claimed {
		t.Fatalf("first post of the week: %+v", d)
	}
	if d := post("p2", "bluesky", "2026-W46"); d.Result == Claimed {
		t.Errorf("a second bluesky post in the same week earned again: %+v", d)
	}
	if d := post("p3", "reddit", "2026-W46"); d.Result != Claimed {
		t.Errorf("a post on another network in the same week: %+v", d)
	}
	if d := post("p4", "bluesky", "2026-W47"); d.Result != Claimed {
		t.Errorf("a post the next week: %+v", d)
	}
	if n := len(r.claims("alice")); n != 3 {
		t.Errorf("claims = %d, want 3", n)
	}
	// a key field that is not a field of the event is refused when the rule is saved.
	bad := weekly
	bad.ID, bad.Limits.KeyField = "bad", "network+month"
	if _, err := r.e.SaveRule(r.ctx, r.slug, bad, "tester"); !refuse.Is(err) {
		t.Errorf("a key naming no field: %v, want a refusal", err)
	}
}

func TestEachBadgeKindOfAReadmeEarnsOncePerRepoAndKindNeverTwice(t *testing.T) {
	r := newRig(t, nil, rule.Rule{ID: "b", Name: "b", Active: true, When: rule.When{Event: "github.badge"}, Then: []rule.Action{pts(10, false)},
		Limits: rule.Limits{Once: rule.Keyed, KeyField: "repo+badge"}})
	badge := func(id, repo, kind string) Decision {
		out := r.eval(catalog.Event{ID: id, Type: "github.badge", Subject: "alice", Fields: map[string]any{"repo": repo, "owner": "alice", "badge": kind},
			Evidence: &catalog.Evidence{Source: "github", URL: "https://github.com/" + repo, NeedsReview: true}})
		return r.decision(out, "b")
	}
	for _, kind := range []string{"built", "profile", "rank"} {
		if d := badge("alice/proj:"+kind, "alice/proj", kind); d.Result != Claimed {
			t.Errorf("%s: %+v", kind, d)
		}
	}
	// the same kind again, however the event is named, is withheld.
	if d := badge("alice/proj:built:again", "alice/proj", "built"); d.Result == Claimed {
		t.Errorf("the same kind twice made a second claim: %+v", d)
	}
	// a second repository, the member's profile repository, has its own.
	if d := badge("alice/alice:profile", "alice/alice", "profile"); d.Result != Claimed {
		t.Errorf("a second repository: %+v", d)
	}
	if n := len(r.claims("alice")); n != 4 {
		t.Errorf("claims = %d, want 4", n)
	}
}

func TestAReferredPersonIsWelcomedOnceAndNeverByThemselves(t *testing.T) {
	r := newRig(t, nil, rule.Rule{ID: "w", Name: "w", Active: true, When: rule.When{Event: catalog.Welcomed}, Then: []rule.Action{pts(10, false)},
		Limits: rule.Limits{Once: rule.Subject}})
	welcome := func(id, subject, sponsor string) (Outcome, error) {
		return r.e.Evaluate(r.ctx, catalog.Event{ID: id, Type: catalog.Welcomed, Subject: subject, Fields: map[string]any{"sponsor": sponsor}})
	}
	out, err := welcome("referral.welcomed:newco", "alice", "bob")
	if err != nil || r.decision(out, "w").Result != Claimed {
		t.Fatalf("welcome: %+v %v", out, err)
	}
	// a second welcome for the same person under another source id is withheld: once per person.
	out, err = welcome("referral.welcomed:otherco", "alice", "carol")
	if err != nil || r.decision(out, "w").Result == Claimed {
		t.Errorf("a second welcome: %+v %v", out, err)
	}
	if _, err := welcome("referral.welcomed:self", "dave", "dave"); !refuse.Is(err) {
		t.Errorf("a person welcoming themselves: %v, want a refusal", err)
	}
	if n := len(r.claims("alice")); n != 1 {
		t.Errorf("alice's claims = %d, want 1", n)
	}
}
