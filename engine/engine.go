// Package engine is the rewards rules engine: the one backend admin.hanzo.ai
// configures and cloud feeds events into.
//
// A rule.Program holds rules. An Event arriving at Evaluate makes a claim for every
// active rule it satisfies, inside that rule's limits and the program's budget. A
// claim made by a manual rule waits for staff; Approve, Reject and Reverse are
// their three acts, and each is on the trail.
//
//	pending --Approve--> approved --(written)--> awarded --Reverse--> reversed
//	pending --Reject--> rejected
//
// EVERYTHING IS WRITTEN ONCE. Claims, decisions, ledger rows and badge rows are
// inserted under ids that are hashes of what they are about, by a conditional
// insert that exactly one writer wins (orm.DB.CreateIfAbsent). There is no
// read-modify-write on any of them and no transaction, so exactly-once holds on
// SQLite and on the shared SQL alike.
//
// THERE IS ONE WAY TO GRANT CREDIT. A credit action goes through Credits, which a
// host wires to commerce's POST /v1/billing/credit (package credit). This package
// has no ledger write of its own for money.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

// Grant is one credit the engine asks the host to grant.
type Grant struct {
	// Org is the org whose ledger it lands in.
	Org   string
	Cents int64
	// Reason and Tag say why; Actor says who approved the claim; Key makes the
	// grant happen at most once however many times the approval runs.
	Reason, Tag, Actor, Key string
}

// Credits is the one way credit is granted.
type Credits interface {
	// Ceiling is the most one grant may be, in cents.
	Ceiling() int64
	// Grant grants g, at most once per g.Key.
	Grant(ctx context.Context, g Grant) error
}

// Mailer delivers one plain-text message to every address in to.
type Mailer interface {
	Send(ctx context.Context, to []string, subject, body string) error
}

// Engine evaluates events and acts on claims. Credits may be nil for a host that
// grants no credit: a rule with a credit action is then refused when it is saved.
// Mail may be nil for a host that sends none; an email action then goes unsent,
// and is logged.
type Engine struct {
	DB      orm.DB
	Credits Credits
	Mail    Mailer
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Log receives what cannot be returned, such as a mail that failed after its
	// claim was awarded. Nil discards it.
	Log func(format string, args ...any)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// Logf logs through the engine's Log, for the layers above it.
func (e *Engine) Logf(format string, args ...any) { e.logf(format, args...) }

// reserve serializes the check-then-claim of Evaluate within this process, so two
// events cannot both pass a limit or the budget that only one of them fits. Across
// replicas the staff queue is the backstop: a claim that should not have been made
// is rejected there.
var reserve sync.Mutex

// ErrNotFound is a program, rule or claim that is not in the store.
var ErrNotFound = errors.New("reward: not found")

// ErrState is an act on a claim that is not in the state the act needs.
var ErrState = errors.New("reward: the claim is not in a state that allows this")

// Decision is what one rule did with one event.
type Decision struct {
	Program     string `json:"program"`
	Rule        string `json:"rule"`
	RuleVersion int    `json:"ruleVersion"`
	// Result is claimed (a claim was made, or in a dry run would be), duplicate
	// (the event already has a claim under this rule) or skipped.
	Result string `json:"result"`
	// Reason says why a rule was skipped.
	Reason string       `json:"reason,omitempty"`
	Claim  *claim.Claim `json:"claim,omitempty"`
}

// Outcome is what an event did.
type Outcome struct {
	Event string `json:"event"`
	// Dry is true for a simulation, which wrote nothing.
	Dry       bool       `json:"dry"`
	Decisions []Decision `json:"decisions"`
}

// Results of a Decision.
const (
	Claimed   = "claimed"
	Duplicate = "duplicate"
	Skipped   = "skipped"
)

// Evaluate makes a claim for every active rule of every active program that reads
// ev and is satisfied by it, within limits and budget. A claim whose rule is auto
// is approved as it is made. The same event again makes no second claim. A
// referral.linked event first links its subject to the sponsor it names, and is
// refused if that cannot be; then the rules read it like any other.
func (e *Engine) Evaluate(ctx context.Context, ev catalog.Event) (Outcome, error) {
	out := Outcome{Event: ev.ID, Decisions: []Decision{}}
	if err := ev.Validate(); err != nil {
		return out, err
	}
	if ev.Type == catalog.Linked {
		if err := e.link(ctx, ev); err != nil {
			return out, err
		}
	}
	progs, err := e.Programs(ctx)
	if err != nil {
		return out, err
	}
	for _, p := range progs {
		if !p.Active {
			continue
		}
		for i := range p.Rules {
			r := &p.Rules[i]
			if !r.Active || r.When.Event != ev.Type {
				continue
			}
			d, err := e.consider(ctx, p, r, ev, false)
			if err != nil {
				return out, err
			}
			out.Decisions = append(out.Decisions, d)
		}
	}
	return out, nil
}

// Simulate is Evaluate for one rule that writes nothing: it answers what ev would
// do under r, whether or not r is active or stored. r is checked against the
// program named slug first.
func (e *Engine) Simulate(ctx context.Context, slug string, r rule.Rule, ev catalog.Event) (Outcome, error) {
	out := Outcome{Event: ev.ID, Dry: true, Decisions: []Decision{}}
	if err := ev.Validate(); err != nil {
		return out, err
	}
	p, err := e.Program(ctx, slug)
	if err != nil {
		return out, err
	}
	if r.Version == 0 {
		r.Version = 1
		if cur := p.Find(r.ID); cur != nil {
			r.Version = cur.Version
		}
	}
	if err := e.check(p, &r); err != nil {
		return out, err
	}
	d, err := e.consider(ctx, p, &r, ev, true)
	if err != nil {
		return out, err
	}
	out.Decisions = append(out.Decisions, d)
	return out, nil
}

// check is rule.Validate and what only the engine knows: whether the host can grant
// the credit the rule names.
func (e *Engine) check(p *rule.Program, r *rule.Rule) error {
	if err := r.Validate(p); err != nil {
		return err
	}
	for _, a := range r.Then {
		if a.Type != rule.Do.Credit {
			continue
		}
		if e.Credits == nil {
			return refuse.New("rule %s: this host grants no credit", r.ID)
		}
		if max := e.Credits.Ceiling(); a.Cents > max {
			return refuse.New("rule %s: credit of %d cents exceeds the ceiling of %d", r.ID, a.Cents, max)
		}
	}
	return nil
}

// consider is one rule's answer to one event. dry reads and writes nothing.
func (e *Engine) consider(ctx context.Context, p *rule.Program, r *rule.Rule, ev catalog.Event, dry bool) (Decision, error) {
	d := Decision{Program: p.Slug(), Rule: r.ID, RuleVersion: r.Version, Result: Skipped}
	if ok, why := r.Matches(ev); !ok {
		d.Reason = why
		return d, nil
	}
	var points, cents int64
	for _, a := range r.Then {
		switch a.Type {
		case rule.Do.Points:
			points += a.Points
		case rule.Do.Credit:
			cents += a.Cents
			switch {
			case e.Credits == nil:
				d.Reason = "this host grants no credit"
				return d, nil
			case ev.Org == "":
				d.Reason = "the event names no org to credit"
				return d, nil
			}
		case rule.Do.Email:
			if ev.Email == "" {
				d.Reason = "the event names no email address"
				return d, nil
			}
		}
	}

	now := e.now()
	c := orm.New[claim.Claim](e.DB)
	c.SetId(claim.ID(ev.ID, r.ID, r.Version))
	c.Origin = claim.OriginOf(ev.ID, r.ID)
	c.Program, c.Rule, c.RuleVersion, c.Pillar, c.Approval = p.Slug(), r.ID, r.Version, r.Pillar, r.Approval
	c.Event, c.EventID, c.EventAt, c.Subject, c.Org, c.Email = ev.Type, ev.ID, ev.At, ev.Subject, ev.Org, ev.Email
	c.Day, c.Month = now.Format("2006-01-02"), now.Format("2006-01")
	c.Points, c.CreditCents, c.Status = points, cents, claim.Pending
	c.Fields, c.Then, c.Evidence = ev.Fields, r.Then, ev.Evidence
	if r.Limits.Once == rule.Keyed {
		c.Slot = r.Limits.Slot(ev.Fields)
	}

	d, err := e.admit(ctx, p, r, c, d, now, dry)
	if err != nil || d.Result != Claimed || dry {
		return d, err
	}
	if r.Approval == rule.Auto {
		if d.Claim, err = e.Approve(ctx, c.Id(), "auto"); err != nil {
			return d, err
		}
	}
	return d, nil
}

// admit checks the claim against the rule's limits and the program's budget and,
// unless dry, writes it. The check and the write are one step in this process.
func (e *Engine) admit(ctx context.Context, p *rule.Program, r *rule.Rule, c *claim.Claim, d Decision, now time.Time, dry bool) (Decision, error) {
	if !dry {
		reserve.Lock()
		defer reserve.Unlock()
	}
	seen, err := e.byOrigin(ctx, c.Origin)
	if err != nil {
		return d, err
	}
	if seen != nil {
		d.Result, d.Claim = Duplicate, seen
		return d, nil
	}
	if why, err := e.withheld(ctx, p, r, c); err != nil || why != "" {
		d.Reason = why
		return d, err
	}
	d.Result, d.Claim = Claimed, c
	if dry {
		return d, nil
	}
	c.CreatedAt, c.UpdatedAt = now, now
	created, err := e.DB.CreateIfAbsent(ctx, c.Key(), c)
	if err != nil {
		return d, fmt.Errorf("reward: write claim %s: %w", c.Id(), err)
	}
	if !created {
		// Another replica made this exact claim between the read and the write.
		won, err := e.Claim(ctx, c.Id())
		if err != nil {
			return d, err
		}
		d.Result, d.Claim = Duplicate, won
	}
	return d, nil
}

// withheld says why the limits of r or the budget of p do not allow c, or "".
func (e *Engine) withheld(ctx context.Context, p *rule.Program, r *rule.Rule, c *claim.Claim) (string, error) {
	l := r.Limits
	if l.Once != rule.Unlimited || l.PerDay > 0 || l.PerMonth > 0 {
		rows, err := orm.TypedQuery[claim.Claim](e.DB).Filter("Subject=", c.Subject).Filter("Rule=", r.ID).GetAll(ctx)
		if err != nil {
			return "", err
		}
		var day, month int
		for _, x := range rows {
			if x.Program != p.Slug() || !x.Status.Live() {
				continue
			}
			switch {
			case l.Once == rule.Subject:
				return "once: the subject has earned this rule", nil
			case l.Once == rule.Keyed && x.Slot == c.Slot:
				return fmt.Sprintf("once: the subject has earned this rule for %s %s", l.KeyField, c.Slot), nil
			}
			if x.Day == c.Day {
				day++
			}
			if x.Month == c.Month {
				month++
			}
		}
		if l.PerDay > 0 && day >= l.PerDay {
			return fmt.Sprintf("perDay: %d claims today already", day), nil
		}
		if l.PerMonth > 0 && month >= l.PerMonth {
			return fmt.Sprintf("perMonth: %d claims this month already", month), nil
		}
	}
	b := p.Budget
	if (b.PointsPerMonth > 0 && c.Points > 0) || (b.CreditCentsPerMonth > 0 && c.CreditCents > 0) {
		rows, err := orm.TypedQuery[claim.Claim](e.DB).Filter("Program=", p.Slug()).Filter("Month=", c.Month).GetAll(ctx)
		if err != nil {
			return "", err
		}
		var points, cents int64
		for _, x := range rows {
			if x.Status.Live() {
				points += x.Points
				cents += x.CreditCents
			}
		}
		if b.PointsPerMonth > 0 && c.Points > 0 && points+c.Points > b.PointsPerMonth {
			return fmt.Sprintf("budget: %d of %d points this month are spent", points, b.PointsPerMonth), nil
		}
		if b.CreditCentsPerMonth > 0 && c.CreditCents > 0 && cents+c.CreditCents > b.CreditCentsPerMonth {
			return fmt.Sprintf("budget: %d of %d credit cents this month are spent", cents, b.CreditCentsPerMonth), nil
		}
	}
	return "", nil
}

// byOrigin is the claim already made for an event under any version of a rule.
func (e *Engine) byOrigin(ctx context.Context, origin string) (*claim.Claim, error) {
	rows, err := orm.TypedQuery[claim.Claim](e.DB).Filter("Origin=", origin).Limit(1).GetAll(ctx)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func staff(reviewer, note string, needNote bool) (string, string, error) {
	reviewer, note = strings.TrimSpace(reviewer), strings.TrimSpace(note)
	if reviewer == "" {
		return "", "", refuse.New("a reviewer is required")
	}
	if needNote && note == "" {
		return "", "", refuse.New("a note is required")
	}
	return reviewer, note, nil
}
