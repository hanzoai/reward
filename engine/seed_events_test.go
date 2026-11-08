package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
)

// The seed program with the real engine: an event of each new type makes the claims
// the seed's rules say, and approving them awards exactly what they name.

type seeded struct {
	t   *testing.T
	ctx context.Context
	e   *Engine
}

func newSeeded(t *testing.T) *seeded {
	t.Helper()
	s := &seeded{t: t, ctx: context.Background(), e: &Engine{DB: openDB(t)}}
	if _, err := s.e.Seed(s.ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// fire sends one event and returns the ids of the rules that claimed it, sorted.
func (s *seeded) fire(id, typ, subject string, fields map[string]any) []string {
	s.t.Helper()
	out, err := s.e.Evaluate(s.ctx, catalog.Event{ID: id, Type: typ, Subject: subject, Fields: fields})
	if err != nil {
		s.t.Fatalf("%s: %v", id, err)
	}
	var got []string
	for _, d := range out.Decisions {
		if d.Result == Claimed {
			got = append(got, d.Rule)
		}
	}
	slices.Sort(got)
	return got
}

// approve approves every pending claim of the subject.
func (s *seeded) approve(subject string) {
	s.t.Helper()
	cs, err := s.e.Claims(s.ctx, Filter{Subject: subject, Status: claim.Pending, Limit: 200})
	if err != nil {
		s.t.Fatal(err)
	}
	for _, c := range cs {
		if _, err := s.e.Approve(s.ctx, c.Id(), "staff"); err != nil {
			s.t.Fatal(err)
		}
	}
}

func (s *seeded) account(subject string) ledger.Account {
	s.t.Helper()
	a, err := s.e.Standing(s.ctx, "hanzo-rewards", subject)
	if err != nil {
		s.t.Fatal(err)
	}
	return a
}

func (s *seeded) claims(subject string) int {
	s.t.Helper()
	cs, err := s.e.Claims(s.ctx, Filter{Subject: subject, Limit: 200})
	if err != nil {
		s.t.Fatal(err)
	}
	return len(cs)
}

func ids(a ...string) []string { return a }

func TestEachCountedEventClaimsItsOwnBadgeAndNothingNextToIt(t *testing.T) {
	s := newSeeded(t)
	for _, c := range counted {
		who := "u-" + c.rule
		if got := s.fire("hit-"+c.rule, c.event, who, map[string]any{c.field: c.n}); !slices.Equal(got, ids(c.rule)) {
			t.Errorf("%s %s=%d claimed %v, want only %s", c.event, c.field, c.n, got, c.rule)
		}
		// one past it is not a milestone of any rule.
		if got := s.fire("past-"+c.rule, c.event, who, map[string]any{c.field: c.n + 1}); len(got) != 0 {
			t.Errorf("%s %s=%d claimed %v", c.event, c.field, c.n+1, got)
		}
		// approving awards the badge and no points.
		s.approve(who)
		a := s.account(who)
		if a.Lifetime != 0 || a.Redeemable != 0 || len(a.Badges) != 1 || a.Badges[0].ID != c.badge || a.Badges[0].Name != c.name || a.Badges[0].Rarity != c.rarity || a.Badges[0].Pillar != c.pillar {
			t.Errorf("%s: account = %+v, want only %s (%s)", c.rule, a, c.name, c.rarity)
		}
	}
}

func TestTenureSixMonthsClaimsSixMonthsOnceAndNeverItsNeighbours(t *testing.T) {
	s := newSeeded(t)
	if got := s.fire("t1", "tenure.reached", "alice", map[string]any{"months": 6}); !slices.Equal(got, ids("tenure-6")) {
		t.Fatalf("months=6 claimed %v, want only Six Months (tenure-6)", got)
	}
	// the same event twice is one claim; another event for the same month is held by the limit.
	if got := s.fire("t1", "tenure.reached", "alice", map[string]any{"months": 6}); len(got) != 0 {
		t.Errorf("the same event again claimed %v", got)
	}
	if got := s.fire("t2", "tenure.reached", "alice", map[string]any{"months": 6}); len(got) != 0 {
		t.Errorf("a second event for month 6 claimed %v", got)
	}
	if n := s.claims("alice"); n != 1 {
		t.Errorf("claims = %d, want 1", n)
	}
	// the other months are other claims, and a month between milestones is none.
	if got := s.fire("t3", "tenure.reached", "alice", map[string]any{"months": 7}); len(got) != 0 {
		t.Errorf("months=7 claimed %v", got)
	}
	if got := s.fire("t4", "tenure.reached", "alice", map[string]any{"months": 12}); !slices.Equal(got, ids("tenure-12")) {
		t.Errorf("months=12 claimed %v", got)
	}
	s.approve("alice")
	a := s.account("alice")
	var held []string
	for _, b := range a.Badges {
		held = append(held, b.Name)
	}
	slices.Sort(held)
	if !slices.Equal(held, []string{"Six Months", "Yearling"}) || a.Lifetime != 0 {
		t.Errorf("alice holds %v with %d points", held, a.Lifetime)
	}
	// another person has their own.
	if got := s.fire("t5", "tenure.reached", "bob", map[string]any{"months": 6}); !slices.Equal(got, ids("tenure-6")) {
		t.Errorf("bob months=6 claimed %v", got)
	}
}

func TestStreakSevenIsAWeekStreakAndEightIsNothing(t *testing.T) {
	s := newSeeded(t)
	if got := s.fire("s7", "streak.reached", "alice", map[string]any{"days": 7}); !slices.Equal(got, ids("streak-7")) {
		t.Errorf("days=7 claimed %v", got)
	}
	if got := s.fire("s8", "streak.reached", "alice", map[string]any{"days": 8}); len(got) != 0 {
		t.Errorf("days=8 claimed %v", got)
	}
	if got := s.fire("s7", "streak.reached", "alice", map[string]any{"days": 7}); len(got) != 0 {
		t.Errorf("days=7 again claimed %v", got)
	}
	// a streak that broke and reached 7 again is the same week streak: once.
	if got := s.fire("s7b", "streak.reached", "alice", map[string]any{"days": 7}); len(got) != 0 {
		t.Errorf("a second days=7 claimed %v", got)
	}
	for days, rule := range map[int]string{30: "streak-30", 100: "streak-100"} {
		if got := s.fire("s", "streak.reached", "carol", map[string]any{"days": days}); !slices.Equal(got, ids(rule)) {
			t.Errorf("days=%d claimed %v, want %s", days, got, rule)
		}
		s.approve("carol")
	}
	s.approve("alice")
	if a := s.account("alice"); len(a.Badges) != 1 || a.Badges[0].Name != "Week Streak" || a.Badges[0].Rarity != "common" {
		t.Errorf("alice = %+v", a)
	}
}

func TestFounderEligibleIsOncePerPersonWhateverTheReason(t *testing.T) {
	s := newSeeded(t)
	if got := s.fire("f1", "founder.eligible", "alice", map[string]any{"reason": "beta"}); !slices.Equal(got, ids("founding-developer")) {
		t.Fatalf("beta claimed %v", got)
	}
	for i, reason := range []string{"alpha", "early", "beta"} {
		if got := s.fire("f-more-"+string(rune('a'+i)), "founder.eligible", "alice", map[string]any{"reason": reason}); len(got) != 0 {
			t.Errorf("a second founder.eligible (%s) claimed %v", reason, got)
		}
	}
	if n := s.claims("alice"); n != 1 {
		t.Errorf("claims = %d, want 1", n)
	}
	// each reason is accepted for someone who has not earned it yet.
	for _, reason := range []string{"alpha", "early"} {
		if got := s.fire("f-"+reason, "founder.eligible", "u-"+reason, map[string]any{"reason": reason}); !slices.Equal(got, ids("founding-developer")) {
			t.Errorf("%s claimed %v", reason, got)
		}
	}
	// a reason that is not one is refused, not claimed.
	_, err := s.e.Evaluate(s.ctx, catalog.Event{ID: "f-bad", Type: "founder.eligible", Subject: "dan", Fields: map[string]any{"reason": "gamma"}})
	if !refuse.Is(err) {
		t.Errorf("err = %v, want a refusal", err)
	}
	s.approve("alice")
	if a := s.account("alice"); len(a.Badges) != 1 || a.Badges[0].Name != "Founding Developer" || a.Badges[0].Rarity != "legendary" || a.Badges[0].Pillar != catalog.Community || a.Lifetime != 0 {
		t.Errorf("alice = %+v", a)
	}
}

func TestFriendOfAFriendPaysOnePointOncePerRefereeAndIsNotRedeemable(t *testing.T) {
	s := newSeeded(t)
	if got := s.fire("n1", "referral.network", "sponsor", map[string]any{"referee": "acme"}); !slices.Equal(got, ids("referral-network")) {
		t.Fatalf("acme claimed %v", got)
	}
	if got := s.fire("n1", "referral.network", "sponsor", map[string]any{"referee": "acme"}); len(got) != 0 {
		t.Errorf("the same event again claimed %v", got)
	}
	if got := s.fire("n2", "referral.network", "sponsor", map[string]any{"referee": "acme"}); len(got) != 0 {
		t.Errorf("acme again in another event claimed %v", got)
	}
	// a second referee is a second +1.
	if got := s.fire("n3", "referral.network", "sponsor", map[string]any{"referee": "globex"}); !slices.Equal(got, ids("referral-network")) {
		t.Fatalf("globex claimed %v", got)
	}
	if n := s.claims("sponsor"); n != 2 {
		t.Errorf("claims = %d, want 2", n)
	}
	s.approve("sponsor")
	if a := s.account("sponsor"); a.Lifetime != 2 || a.Redeemable != 0 || len(a.Badges) != 0 {
		t.Errorf("sponsor = %+v, want 2 lifetime points, none redeemable, no badge", a)
	}
}

func TestReferralMilestoneFiresOnTheMultipleItNamesAndOnNoOther(t *testing.T) {
	s := newSeeded(t)
	for i, c := range []struct {
		count int
		want  []string
	}{
		{5, ids("referral-milestone-5")}, {4, nil}, {6, nil}, {10, ids("referral-milestone-10")}, {11, nil}, {0, nil}, {-5, nil},
		{50, ids("referral-milestone-50")}, {55, nil}, {100, ids("referral-milestone-100")}, {550, ids("referral-milestone-550")}, {600, nil},
	} {
		got := s.fire("m"+string(rune('a'+i)), "referral.milestone", "sponsor", map[string]any{"count": c.count})
		if !slices.Equal(got, c.want) {
			t.Errorf("count=%d claimed %v, want %v", c.count, got, c.want)
		}
	}
	// the same count in another event is the same milestone.
	if got := s.fire("m-again", "referral.milestone", "sponsor", map[string]any{"count": 5}); len(got) != 0 {
		t.Errorf("count=5 again claimed %v", got)
	}
	s.approve("sponsor")
	a := s.account("sponsor")
	var held []string
	for _, b := range a.Badges {
		held = append(held, b.Name)
	}
	slices.Sort(held)
	if !slices.Equal(held, []string{"Cosmos I", "Cosmos X", "Signal", "Spark", "Supernova"}) || a.Lifetime != 0 {
		t.Errorf("sponsor holds %v with %d points", held, a.Lifetime)
	}
}

func TestAPersonAtNineNineNineIsPhotonNineAndAtAThousandWaveTen(t *testing.T) {
	s := newSeeded(t)
	back := func(source string, points int64) {
		t.Helper()
		if _, err := s.e.Backfill(s.ctx, "hanzo-rewards", []ledger.Past{{Subject: "alice", Source: source, Points: points}}, "z"); err != nil {
			t.Fatal(err)
		}
	}
	if a := s.account("alice"); a.Level.Level != 0 || a.Level.Name != "Photon 0" || a.Band != "Photon" || a.Next == nil || a.Next.Name != "Photon 1" {
		t.Errorf("nobody yet: %+v", a)
	}
	back("usage.tokens", 999)
	a := s.account("alice")
	if a.Lifetime != 999 || a.Level.Level != 9 || a.Level.Name != "Photon 9" || a.Band != "Photon" || a.Level.Band != "Photon" || a.Next == nil || a.Next.Name != "Wave 10" || a.Next.MinLifetimePoints != 1000 {
		t.Errorf("999 points: %+v", a)
	}
	back("github.star", 1)
	a = s.account("alice")
	if a.Lifetime != 1000 || a.Level.Level != 10 || a.Level.Name != "Wave 10" || a.Band != "Wave" || a.Level.Band != "Wave" {
		t.Errorf("1000 points: %+v", a)
	}
}
