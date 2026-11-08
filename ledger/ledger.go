// Package ledger is the append-only points ledger and the trail of staff acts.
//
// Points and badges are Entry rows, written once under an id that is a hash of the
// claim and the action, so an approval that runs twice writes once. A reversal is
// more rows, never an edit. A subject's lifetime points (levels) and redeemable
// points (credit-eligible) are folds over their rows (Standing).
package ledger

import (
	"context"
	"time"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/rule"
)

// Kinds in the store.
const (
	Kind      = "reward-entry"
	TrailKind = "reward-trail"
)

func init() {
	orm.Register[Entry](Kind, orm.WithStringKey[Entry]())
	orm.Register[Trail](TrailKind)
}

// What an Entry does.
const (
	// Award is a row that adds to a subject's ledger.
	Award = "award"
	// Reversal is a row that undoes an Award: a compensating entry, never an edit.
	Reversal = "reversal"
	// Legacy is a row of points a subject earned before the ledger, grandfathered
	// in by a backfill. It counts toward lifetime points, levels and rank and never
	// toward redeemable points: it is not an award of any rule, and credit is only
	// ever for what a rule awarded.
	Legacy = "legacy"
)

// Entry is one row of the ledger. A row is written once and never changed. An
// entry carries points, or a badge, never both.
type Entry struct {
	orm.Model[Entry]

	Program     string `json:"program" orm:"index"`
	Subject     string `json:"subject" orm:"index"`
	Claim       string `json:"claim" orm:"index"`
	Rule        string `json:"rule"`
	RuleVersion int    `json:"ruleVersion"`

	// Act is Award or Reversal.
	Act string `json:"kind"`
	// Points is signed: a reversal of an award of 50 is -50.
	Points int64 `json:"points,omitempty"`
	// Redeemable says whether the points count toward the redeemable balance as
	// well as the lifetime one.
	Redeemable bool `json:"redeemable,omitempty"`
	// Badge is the badge a row awards or takes back.
	Badge string `json:"badge,omitempty"`
	// Reverses is the id of the entry a reversal undoes.
	Reverses string `json:"reverses,omitempty"`

	// Source, Evidence and By are what a legacy row says about itself: the thing
	// the points were earned for (one of Sources), what the count was taken from,
	// and the staff member who ran the backfill.
	Source   string `json:"source,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	By       string `json:"by,omitempty"`

	At time.Time `json:"at"`
}

// Sources are the things legacy points were earned for, as the beta board counted
// them: a source is named so a backfill cannot invent one.
var Sources = []string{
	"usage.tokens",    // 1 per 100K tokens
	"referral.signup", // 1 per referred signup
	"referral.paying", // 20 per referred org with spend
	"github.star",     // 1 per star
	"github.follow",   // 1 per follow
	"github.fork",     // 1 per fork
	"bounty",          // the points of a bounty
}

// Past is one row of a backfill: points a subject earned before the ledger.
type Past struct {
	Subject string `json:"subject"`
	// Source is one of Sources. A subject has one row per source: a second row for
	// the same source is the same row.
	Source   string `json:"source"`
	Points   int64  `json:"points"`
	Evidence string `json:"evidence"`
}

// LegacyID is the id of the legacy row for a subject's source in a program.
func LegacyID(program, subject, source string) string {
	return "ent_" + claim.Digest("legacy", program, subject, source)
}

// EntryID is the id of the entry of act for the slot of a claim.
func EntryID(c, slot, act string) string { return "ent_" + claim.Digest("entry", c, slot, act) }

// Account is one subject's standing in one program: balances, level and badges,
// all folds over the ledger.
type Account struct {
	Program string `json:"program"`
	Subject string `json:"subject"`
	// Lifetime is every point ever awarded less every one reversed. Nothing the
	// subject does with their points lowers it, so levels never fall by spending.
	Lifetime int64 `json:"lifetime"`
	// Redeemable is the part of Lifetime the awards marked credit-eligible.
	Redeemable int64      `json:"redeemable"`
	Level      rule.Level `json:"level"`
	// Next is the level above, with the points it needs; nil at the top.
	Next   *rule.Level `json:"next,omitempty"`
	Badges []Held      `json:"badges"`
}

// Held is a badge a subject holds, and when they first earned it.
type Held struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Pillar   catalog.Pillar `json:"pillar"`
	Rarity   rule.Rarity    `json:"rarity"`
	EarnedAt time.Time      `json:"earnedAt"`
}

// Rows are every ledger row of a subject in a program.
func Rows(ctx context.Context, db orm.DB, program, subject string) ([]*Entry, error) {
	return orm.TypedQuery[Entry](db).Filter("Subject=", subject).Filter("Program=", program).GetAll(ctx)
}

// Standing computes the subject's account in program p.
func Standing(ctx context.Context, db orm.DB, p *rule.Program, subject string) (Account, error) {
	a := Account{Program: p.Slug(), Subject: subject, Badges: []Held{}}
	rows, err := Rows(ctx, db, p.Slug(), subject)
	if err != nil {
		return a, err
	}
	undone := map[string]bool{}
	for _, x := range rows {
		if x.Act == Reversal {
			undone[x.Reverses] = true
		}
	}
	held := map[string]int{}
	since := map[string]time.Time{}
	for _, x := range rows {
		a.Lifetime += x.Points
		if x.Redeemable && x.Act != Legacy {
			a.Redeemable += x.Points
		}
		if x.Badge == "" {
			continue
		}
		if x.Act == Award {
			held[x.Badge]++
			if !undone[x.Id()] && (since[x.Badge].IsZero() || x.At.Before(since[x.Badge])) {
				since[x.Badge] = x.At
			}
		} else {
			held[x.Badge]--
		}
	}
	a.Level, a.Next = p.LevelOf(a.Lifetime)
	for _, b := range p.Badges {
		if held[b.ID] > 0 {
			r := b.Rarity
			if r == "" {
				r = rule.Common
			}
			a.Badges = append(a.Badges, Held{ID: b.ID, Name: b.Name, Pillar: b.Pillar, Rarity: r, EarnedAt: since[b.ID]})
		}
	}
	return a, nil
}

// Trail is the record of one privileged act: who did what to what, and when. Every
// staff decision on a claim and every change to a program is one. A row is written
// once and never changed.
type Trail struct {
	orm.Model[Trail]

	Actor  string `json:"actor" orm:"index"`
	Verb   string `json:"verb" orm:"index"`
	Object string `json:"object" orm:"index"`
	// Detail is what the act changed, for the reader of the trail.
	Detail map[string]any `json:"detail"`
	At     time.Time      `json:"at"`
}

// Record writes a trail row.
func Record(ctx context.Context, db orm.DB, actor, verb, object string, detail map[string]any, at time.Time) error {
	t := orm.New[Trail](db)
	t.Actor, t.Verb, t.Object, t.Detail, t.At = actor, verb, object, detail, at
	return t.CreateCtx(ctx)
}

// Acts lists the trail rows of a verb about an object, oldest first.
func Acts(ctx context.Context, db orm.DB, verb, object string) ([]*Trail, error) {
	q := orm.TypedQuery[Trail](db)
	if verb != "" {
		q = q.Filter("Verb=", verb)
	}
	if object != "" {
		q = q.Filter("Object=", object)
	}
	return q.Order("at").GetAll(ctx)
}
