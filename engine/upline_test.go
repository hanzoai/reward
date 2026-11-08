package engine

import (
	"fmt"
	"slices"
	"testing"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

// The upline rig: a program that pays 10%, 5% and 2% up the sponsor chain, a star
// rule worth 100 points, and members linked by real referral.linked events.

func withUpline(p *rule.Program) { p.Upline = []int{10, 5, 2} }

func (r *rig) link(subject, sponsor string) error {
	_, err := r.e.Evaluate(r.ctx, catalog.Event{ID: "link-" + subject + "-" + sponsor, Type: catalog.Linked, Subject: subject, Fields: map[string]any{"sponsor": sponsor}})
	return err
}

// chain links names so that each is the next one's sponsor: chain(a, b, c) is a<-b<-c.
func (r *rig) chain(names ...string) {
	r.t.Helper()
	for i := 1; i < len(names); i++ {
		if err := r.link(names[i], names[i-1]); err != nil {
			r.t.Fatalf("link %s to %s: %v", names[i], names[i-1], err)
		}
	}
}

func (r *rig) sponsor(subject string) string {
	r.t.Helper()
	s, err := r.e.sponsorOf(r.ctx, subject)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// award makes subject's claim for the event id under rule s and has staff approve it.
func (r *rig) award(id, subject string) *claim.Claim {
	r.t.Helper()
	c := r.decision(r.eval(r.starEvent(id, subject, "x")), "s").Claim
	got, err := r.e.Approve(r.ctx, c.Id(), "staff")
	if err != nil {
		r.t.Fatalf("approve %s: %v", id, err)
	}
	return got
}

// uplineRows are the subject's rows that were paid out of another member's award.
func (r *rig) uplineRows(subject string) []*ledger.Entry {
	r.t.Helper()
	var out []*ledger.Entry
	for _, x := range r.rows(subject) {
		if x.Act == ledger.Upline {
			out = append(out, x)
		}
	}
	return out
}

func TestAnAwardPaysThreeLevelsOfUplineAndNoMore(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(100, true)))
	r.chain("a", "b", "c", "d", "e")
	c := r.award("e1", "e")

	for subject, want := range map[string]int64{"e": 100, "d": 10, "c": 5, "b": 2, "a": 0} {
		if got := r.account(subject).Lifetime; got != want {
			t.Errorf("%s lifetime = %d, want %d", subject, got, want)
		}
	}
	if rows := r.rows("a"); len(rows) != 0 {
		t.Errorf("the fourth sponsor up was paid: %+v", rows)
	}
	for i, subject := range []string{"d", "c", "b"} {
		rows := r.uplineRows(subject)
		if len(rows) != 1 {
			t.Fatalf("%s upline rows = %d, want 1", subject, len(rows))
		}
		x := rows[0]
		level := i + 1
		if x.Claim != c.Id() || x.Level != level || x.Redeemable || x.Subject != subject || x.Id() != ledger.EntryID(c.Id(), fmt.Sprint(level), ledger.Upline) {
			t.Errorf("%s upline row = %+v, want claim %s level %d, not redeemable", subject, x, c.Id(), level)
		}
	}
}

func TestUplineRowsAreNotClaimsAndPayNoUpline(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(100, true)))
	r.chain("a", "b", "c", "d", "e")
	r.award("e1", "e")

	for _, subject := range []string{"a", "b", "c", "d"} {
		if n := len(r.claims(subject)); n != 0 {
			t.Errorf("%s has %d claims; upline is paid without one", subject, n)
		}
	}
	// one award of e's and three upline rows: nothing was paid out of the upline.
	var all int
	for _, subject := range []string{"a", "b", "c", "d", "e"} {
		all += len(r.rows(subject))
	}
	if all != 4 {
		t.Errorf("ledger rows = %d, want 4", all)
	}
}

func TestUplineRoundsDownEachShare(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(15, true)))
	r.chain("a", "b", "c", "d")
	r.award("e1", "d")

	// 15 points at 10%, 5% and 2% is 1.5, 0.75 and 0.3.
	if rows := r.uplineRows("c"); len(rows) != 1 || rows[0].Points != 1 {
		t.Errorf("level 1 = %+v, want 1 point", rows)
	}
	for _, subject := range []string{"b", "a"} {
		if rows := r.rows(subject); len(rows) != 0 {
			t.Errorf("%s was written a row for a share that rounds to 0: %+v", subject, rows)
		}
	}
}

func TestAProgramWithNoUplineOrAClaimWithNoPointsPaysNoUpline(t *testing.T) {
	none := newRig(t, nil, star("s", pts(100, true)))
	none.chain("a", "b")
	none.award("e1", "b")
	if rows := none.rows("a"); len(rows) != 0 {
		t.Errorf("a program with no upline paid %+v", rows)
	}

	badgeOnly := newRig(t, withUpline, star("s", badge("b1")))
	badgeOnly.chain("a", "b")
	badgeOnly.award("e1", "b")
	if rows := badgeOnly.rows("a"); len(rows) != 0 {
		t.Errorf("a claim of no points paid %+v", rows)
	}
}

func TestUplineCountsTowardLifetimeAndLevelAndNeverTowardRedeemable(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(1000, true)))
	r.chain("sponsor", "member")
	r.award("e1", "member")

	a := r.account("sponsor")
	if a.Lifetime != 100 || a.Redeemable != 0 || a.Level.Name != "Builder" {
		t.Errorf("sponsor = %+v, want 100 lifetime, 0 redeemable, level Builder", a)
	}
	if m := r.account("member"); m.Lifetime != 1000 || m.Redeemable != 1000 {
		t.Errorf("member = %+v", m)
	}
	if r.bank.calls != 0 {
		t.Errorf("upline touched the credit book %d times", r.bank.calls)
	}
	// a row marked redeemable by hand is still not counted as such.
	x := r.uplineRows("sponsor")[0]
	x.Redeemable = true
	if err := x.PutCtx(r.ctx); err != nil {
		t.Fatal(err)
	}
	if a := r.account("sponsor"); a.Redeemable != 0 {
		t.Errorf("a flagged upline row counted as redeemable: %+v", a)
	}
}

func TestReverseTakesBackTheUplineWithTheClaim(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(100, true)))
	r.chain("a", "b", "c", "d", "e")
	c := r.award("e1", "e")

	for range 2 {
		if _, err := r.e.Reverse(r.ctx, c.Id(), "staff", "fraud"); err != nil {
			t.Fatal(err)
		}
	}
	for _, subject := range []string{"e", "d", "c", "b", "a"} {
		if got := r.account(subject).Lifetime; got != 0 {
			t.Errorf("%s lifetime = %d after the reversal", subject, got)
		}
	}
	for _, subject := range []string{"d", "c", "b"} {
		rows := r.rows(subject)
		if len(rows) != 2 {
			t.Fatalf("%s rows = %d, want the upline row and its reversal", subject, len(rows))
		}
		slices.SortFunc(rows, func(x, y *ledger.Entry) int {
			if x.Act == y.Act {
				return 0
			}
			if x.Act == ledger.Upline {
				return -1
			}
			return 1
		})
		paid, undo := rows[0], rows[1]
		if paid.Act != ledger.Upline || undo.Act != ledger.Reversal || undo.Reverses != paid.Id() || undo.Points != -paid.Points || undo.Subject != subject || undo.Level != paid.Level || undo.Redeemable {
			t.Errorf("%s: paid %+v, reversal %+v", subject, paid, undo)
		}
	}
	if n := r.trail("claim.reversed", "claim/"+c.Id()); n != 1 {
		t.Errorf("reverse trail records = %d, want 1", n)
	}
}

func TestSelfSponsorshipAndCyclesAreRefusedAndLinkNothing(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(100, true)))

	if err := r.link("a", "a"); !refuse.Is(err) {
		t.Errorf("self-sponsorship: %v, want a refusal", err)
	}
	// a sponsors b, then b sponsors a.
	r.chain("a", "b")
	if err := r.link("a", "b"); !refuse.Is(err) {
		t.Errorf("a two-member cycle: %v, want a refusal", err)
	}
	// a->b->c, then c sponsors a.
	r.chain("a", "b", "c")
	if err := r.link("a", "c"); !refuse.Is(err) {
		t.Errorf("a three-member cycle: %v, want a refusal", err)
	}
	// a long chain closed at its far end.
	names := make([]string, 60)
	for i := range names {
		names[i] = fmt.Sprintf("m%02d", i)
	}
	r.chain(names...)
	if err := r.link(names[0], names[len(names)-1]); !refuse.Is(err) {
		t.Errorf("a cycle through 60 members: %v, want a refusal", err)
	}
	for _, subject := range []string{"a", names[0]} {
		if s := r.sponsor(subject); s != "" {
			t.Errorf("%s was linked to %s by a refused event", subject, s)
		}
	}
}

func TestASecondLinkForAMemberChangesNothing(t *testing.T) {
	r := newRig(t, nil,
		rule.Rule{ID: "linked", Name: "linked", Active: true, When: rule.When{Event: catalog.Linked}, Then: []rule.Action{pts(5, false)}},
		rule.Rule{ID: "from-a", Name: "from a", Active: true, When: rule.When{Event: catalog.Linked}, If: []rule.Condition{{Field: "sponsor", Op: rule.Eq, Value: "a"}}, Then: []rule.Action{pts(1, false)}},
	)
	event := func(id, sponsor string) catalog.Event {
		return catalog.Event{ID: id, Type: catalog.Linked, Subject: "b", Fields: map[string]any{"sponsor": sponsor}}
	}
	first := r.eval(event("l1", "a"))
	if d := r.decision(first, "linked"); d.Result != Claimed {
		t.Fatalf("the rules did not see the event: %+v", d)
	}
	if d := r.decision(first, "from-a"); d.Result != Claimed {
		t.Errorf("a rule could not read the sponsor: %+v", d)
	}

	// the same event again is a duplicate claim, as for any event.
	again := r.eval(event("l1", "a"))
	if d := r.decision(again, "linked"); d.Result != Duplicate {
		t.Errorf("redelivery = %+v", d)
	}
	// another event for the same member names another sponsor: no error, no change.
	other := r.eval(event("l2", "c"))
	if got := r.sponsor("b"); got != "a" {
		t.Errorf("sponsor = %q, the first must stand", got)
	}
	if d := r.decision(other, "from-a"); d.Result != Skipped {
		t.Errorf("the second event's sponsor was read as a: %+v", d)
	}
	if d := r.decision(other, "linked"); d.Result != Claimed {
		t.Errorf("a new event id is a new claim: %+v", d)
	}
}

func TestALinkNeedsASponsor(t *testing.T) {
	r := newRig(t, nil)
	for name, sponsor := range map[string]string{"empty": "", "blank": "  ", "two lines": "a\nb"} {
		if err := r.link("b", sponsor); !refuse.Is(err) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
	if s := r.sponsor("b"); s != "" {
		t.Errorf("sponsor = %q", s)
	}
}

func TestApproveRetriedWritesTheUplineOnce(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(100, true), cash(300)))
	r.chain("a", "b", "c")
	ev := r.starEvent("e1", "c", "x")
	ev.Org = "acme"
	c := r.decision(r.eval(ev), "s").Claim

	r.bank.broken = true
	if _, err := r.e.Approve(r.ctx, c.Id(), "staff"); err == nil {
		t.Fatal("approve hid a failed credit")
	}
	r.bank.broken = false
	for range 3 {
		if got, err := r.e.Approve(r.ctx, c.Id(), "staff"); err != nil || got.Status != claim.Awarded {
			t.Fatalf("approve: %v %+v", err, got)
		}
	}
	// writing the claim again, as a retry after a partial failure does, rewrites the same ids.
	if err := r.e.write(r.ctx, c); err != nil {
		t.Fatal(err)
	}
	for subject, want := range map[string]int64{"b": 10, "a": 5} {
		if rows := r.uplineRows(subject); len(rows) != 1 || rows[0].Points != want {
			t.Errorf("%s upline rows = %+v, want one of %d", subject, rows, want)
		}
	}
}

func TestBackfilledPointsPayNoUpline(t *testing.T) {
	r := newRig(t, withUpline, star("s", pts(100, true)))
	r.chain("a", "b")
	if _, err := r.e.Backfill(r.ctx, r.slug, []ledger.Past{{Subject: "b", Source: "usage.tokens", Points: 500}}, "z"); err != nil {
		t.Fatal(err)
	}
	if rows := r.rows("a"); len(rows) != 0 {
		t.Errorf("a legacy row paid upline: %+v", rows)
	}
}

func TestUplineIsUpToThreePercentagesFromZeroToAHundred(t *testing.T) {
	r := newRig(t, nil, star("s", pts(1, false)))
	cur, _ := r.e.Program(r.ctx, r.slug)
	save := func(upline []int) (*rule.Program, error) {
		in := rule.Program{Name: cur.Name, Active: cur.Active, Levels: cur.Levels, Badges: cur.Badges, Templates: cur.Templates, Upline: upline}
		return r.e.SaveProgram(r.ctx, r.slug, in, "tester")
	}
	for name, upline := range map[string][]int{"four levels": {10, 5, 2, 1}, "over a hundred": {101}, "negative": {10, -1}} {
		if _, err := save(upline); !refuse.Is(err) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	if got, _ := r.e.Program(r.ctx, r.slug); len(got.Upline) != 0 || got.Version != 1 {
		t.Errorf("a refused save changed the program: %+v", got)
	}

	for _, upline := range [][]int{{10, 5, 2}, {100}, {0, 0, 0}} {
		got, err := save(upline)
		if err != nil || !slices.Equal(got.Upline, upline) {
			t.Fatalf("save %v: %v %+v", upline, err, got)
		}
		if stored, _ := r.e.Program(r.ctx, r.slug); !slices.Equal(stored.Upline, upline) {
			t.Errorf("stored upline = %v, want %v", stored.Upline, upline)
		}
	}
	// a save that changes only the upline is a change; one that changes nothing is not.
	v, _ := r.e.Program(r.ctx, r.slug)
	if again, err := save([]int{0, 0, 0}); err != nil || again.Version != v.Version {
		t.Errorf("saving the same upline raised the version: %v %+v", err, again)
	}
	if cleared, err := save(nil); err != nil || len(cleared.Upline) != 0 || cleared.Version != v.Version+1 {
		t.Errorf("clearing the upline: %v %+v", err, cleared)
	}
}
