package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

// load reads the entity of type T with id, or ErrNotFound.
func load[T any](ctx context.Context, db orm.DB, id string) (*T, error) {
	x := orm.New[T](db)
	if err := db.Get(ctx, db.NewKey(orm.Kind[T](), id, 0, nil), x); err != nil {
		if orm.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if m, ok := any(x).(interface{ SetId(string) }); ok {
		m.SetId(id)
	}
	return x, nil
}

// steps are the steps decided about a claim.
func (e *Engine) steps(ctx context.Context, id string) ([]*claim.Step, error) {
	return orm.TypedQuery[claim.Step](e.DB).Filter("Claim=", id).GetAll(ctx)
}

// read is the claim and the status its steps say it has, settled onto the row.
func (e *Engine) read(ctx context.Context, id string) (*claim.Claim, []*claim.Step, error) {
	c, err := load[claim.Claim](ctx, e.DB, id)
	if err != nil {
		return nil, nil, err
	}
	steps, err := e.steps(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return c, steps, e.settle(ctx, c, steps)
}

// settle writes the status the steps fold to onto the claim row, if it is behind.
// The row's other fields never change, so two settles can only disagree about a
// status, and each writes the fold it read.
func (e *Engine) settle(ctx context.Context, c *claim.Claim, steps []*claim.Step) error {
	st := claim.Fold(steps)
	who, at, note := claim.Latest(steps)
	if c.Status == st && c.Reviewer == who && c.ReviewedAt.Equal(at) && c.Note == note {
		return nil
	}
	c.Status, c.Reviewer, c.ReviewedAt, c.Note = st, who, at, note
	if err := c.PutCtx(ctx); err != nil {
		return fmt.Errorf("reward: settle claim %s: %w", c.Id(), err)
	}
	return nil
}

// step writes step act of claim id, once. It reports whether this call wrote it;
// false means another writer got there first and the step stands as they wrote it.
func (e *Engine) step(ctx context.Context, id, act, verdict, reviewer, note string) (bool, error) {
	s := orm.New[claim.Step](e.DB)
	s.SetId(claim.StepID(id, act))
	s.Claim, s.Act, s.Verdict, s.Reviewer, s.Note = id, act, verdict, reviewer, note
	s.At = e.now()
	s.CreatedAt, s.UpdatedAt = s.At, s.At
	created, err := e.DB.CreateIfAbsent(ctx, s.Key(), s)
	if err != nil {
		return false, fmt.Errorf("reward: write %s of claim %s: %w", act, id, err)
	}
	return created, nil
}

// Claim reads the claim id with its status settled to what its steps say.
func (e *Engine) Claim(ctx context.Context, id string) (*claim.Claim, error) {
	c, _, err := e.read(ctx, id)
	return c, err
}

// Approve decides that the claim is awarded and writes what it earns: the points
// and badges to the ledger, the credit through Credits, each exactly once. A claim
// already approved is finished, and one already awarded is returned as it is, so a
// retry after a failure is always safe.
func (e *Engine) Approve(ctx context.Context, id, reviewer string) (*claim.Claim, error) {
	reviewer, _, err := staff(reviewer, "", false)
	if err != nil {
		return nil, err
	}
	c, steps, err := e.read(ctx, id)
	if err != nil {
		return nil, err
	}
	decided := false
	if claim.Fold(steps) == claim.Pending {
		if decided, err = e.step(ctx, id, claim.Decision, claim.Approve, reviewer, ""); err != nil {
			return nil, err
		}
		if steps, err = e.steps(ctx, id); err != nil {
			return nil, err
		}
	}
	switch st := claim.Fold(steps); st {
	case claim.Approved, claim.Awarded:
	default:
		return nil, fmt.Errorf("%w: a %s claim cannot be approved", ErrState, st)
	}
	if decided {
		if err := e.settle(ctx, c, steps); err != nil {
			return nil, err
		}
		if err := e.trail(ctx, reviewer, "claim.approved", c, ""); err != nil {
			return c, err
		}
	}
	if claim.Fold(steps) == claim.Awarded {
		return c, e.settle(ctx, c, steps)
	}
	if err := e.write(ctx, c); err != nil {
		return c, err
	}
	won, err := e.step(ctx, id, claim.Award, "", c.Reviewer, "")
	if err != nil {
		return c, err
	}
	if steps, err = e.steps(ctx, id); err != nil {
		return c, err
	}
	if err := e.settle(ctx, c, steps); err != nil {
		return c, err
	}
	if won {
		e.mail(ctx, c)
	}
	return c, nil
}

// write puts everything the claim earns in the ledger and the credit book. Every
// write is keyed by the claim and the action, so running it twice writes once.
func (e *Engine) write(ctx context.Context, c *claim.Claim) error {
	for i, a := range c.Then {
		switch a.Type {
		case rule.Do.Points:
			if a.Points == 0 {
				continue
			}
			x := orm.New[ledger.Entry](e.DB)
			x.Act, x.Points, x.Redeemable = ledger.Award, a.Points, a.Redeemable
			if err := e.put(ctx, c, fmt.Sprint(i), x); err != nil {
				return err
			}
		case rule.Do.Badge:
			x := orm.New[ledger.Entry](e.DB)
			x.Act, x.Badge = ledger.Award, a.Badge
			if err := e.put(ctx, c, fmt.Sprint(i), x); err != nil {
				return err
			}
		case rule.Do.Credit:
			if e.Credits == nil {
				return fmt.Errorf("reward: claim %s grants credit and this host cannot", c.Id())
			}
			if err := e.Credits.Grant(ctx, Grant{
				Org: c.Org, Cents: a.Cents, Actor: c.Reviewer,
				Reason: fmt.Sprintf("program %s, rule %s", c.Program, c.Rule),
				Tag:    "reward:" + c.Program + "/" + c.Rule,
				Key:    fmt.Sprintf("reward:%s:%d", c.Id(), i),
			}); err != nil {
				return fmt.Errorf("reward: credit for claim %s: %w", c.Id(), err)
			}
		}
	}
	return nil
}

// put writes the ledger row x for claim c, once: its id is a hash of the claim, the
// slot it fills and what it does, so a second write of it is a no-op.
func (e *Engine) put(ctx context.Context, c *claim.Claim, slot string, x *ledger.Entry) error {
	x.SetId(ledger.EntryID(c.Id(), slot, x.Act))
	x.Program, x.Subject, x.Claim, x.Rule, x.RuleVersion = c.Program, c.Subject, c.Id(), c.Rule, c.RuleVersion
	x.At = e.now()
	x.CreatedAt, x.UpdatedAt = x.At, x.At
	if _, err := e.DB.CreateIfAbsent(ctx, x.Key(), x); err != nil {
		return fmt.Errorf("reward: write ledger row %s: %w", x.Id(), err)
	}
	return nil
}

// Reject refuses a pending claim. Nothing is written to the ledger.
func (e *Engine) Reject(ctx context.Context, id, reviewer, note string) (*claim.Claim, error) {
	reviewer, note, err := staff(reviewer, note, true)
	if err != nil {
		return nil, err
	}
	c, steps, err := e.read(ctx, id)
	if err != nil {
		return nil, err
	}
	decided := false
	if claim.Fold(steps) == claim.Pending {
		if decided, err = e.step(ctx, id, claim.Decision, claim.Reject, reviewer, note); err != nil {
			return nil, err
		}
		if steps, err = e.steps(ctx, id); err != nil {
			return nil, err
		}
	}
	if st := claim.Fold(steps); st != claim.Rejected {
		return nil, fmt.Errorf("%w: a %s claim cannot be rejected", ErrState, st)
	}
	if err := e.settle(ctx, c, steps); err != nil {
		return nil, err
	}
	if decided {
		return c, e.trail(ctx, reviewer, "claim.rejected", c, note)
	}
	return c, nil
}

// Reverse undoes an awarded claim with compensating entries: the points and badges
// it wrote are taken back by rows of their own, and the rows it wrote stay. Credit
// already granted is money in an org's ledger and is not taken back here, so a
// claim that granted any cannot be reversed.
func (e *Engine) Reverse(ctx context.Context, id, reviewer, note string) (*claim.Claim, error) {
	reviewer, note, err := staff(reviewer, note, true)
	if err != nil {
		return nil, err
	}
	c, steps, err := e.read(ctx, id)
	if err != nil {
		return nil, err
	}
	switch st := claim.Fold(steps); st {
	case claim.Reversed:
		return c, nil
	case claim.Awarded:
	default:
		return nil, fmt.Errorf("%w: a %s claim cannot be reversed", ErrState, st)
	}
	for _, a := range c.Then {
		if a.Type == rule.Do.Credit {
			return nil, refuse.New("claim %s granted credit, which is not taken back here", c.Id())
		}
	}
	rows, err := orm.TypedQuery[ledger.Entry](e.DB).Filter("Claim=", id).GetAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range rows {
		if x.Act != ledger.Award {
			continue
		}
		undo := orm.New[ledger.Entry](e.DB)
		undo.Act, undo.Points, undo.Redeemable, undo.Badge, undo.Reverses = ledger.Reversal, -x.Points, x.Redeemable, x.Badge, x.Id()
		if err := e.put(ctx, c, x.Id(), undo); err != nil {
			return nil, err
		}
	}
	won, err := e.step(ctx, id, claim.Reversal, "", reviewer, note)
	if err != nil {
		return nil, err
	}
	if steps, err = e.steps(ctx, id); err != nil {
		return nil, err
	}
	if err := e.settle(ctx, c, steps); err != nil {
		return nil, err
	}
	if won {
		return c, e.trail(ctx, reviewer, "claim.reversed", c, note)
	}
	return c, nil
}

// trail records a staff act on a claim.
func (e *Engine) trail(ctx context.Context, actor, verb string, c *claim.Claim, note string) error {
	return e.record(ctx, actor, verb, "claim/"+c.Id(), map[string]any{
		"program": c.Program, "rule": c.Rule, "ruleVersion": c.RuleVersion,
		"subject": c.Subject, "points": c.Points, "creditCents": c.CreditCents, "note": note,
	})
}

// record writes a trail row. A change that cannot be traced is reported, though it
// was made: the caller learns it, and the same call again leaves it unchanged.
func (e *Engine) record(ctx context.Context, actor, verb, object string, detail map[string]any) error {
	if err := ledger.Record(ctx, e.DB, actor, verb, object, detail, e.now()); err != nil {
		e.logf("reward: %s %s was done and its trail record was not: %v", verb, object, err)
		return fmt.Errorf("%s %s was done and its trail record was not written: %w", verb, object, err)
	}
	return nil
}

// mail sends the claim's email actions once the claim is awarded. A mail that fails
// is logged and nothing else: the award is final and the host's rail may be down,
// and a retry of Approve must not send twice.
func (e *Engine) mail(ctx context.Context, c *claim.Claim) {
	if c.Email == "" {
		return
	}
	p, err := e.Program(ctx, c.Program)
	if err != nil {
		e.logf("reward: mail for claim %s: %v", c.Id(), err)
		return
	}
	for _, a := range c.Then {
		if a.Type != rule.Do.Email {
			continue
		}
		t := p.Template(a.Template)
		if t == nil {
			e.logf("reward: claim %s names the template %s and the program has none", c.Id(), a.Template)
			continue
		}
		subject, body, err := render(*t, p, c)
		if err == nil {
			if e.Mail == nil {
				err = errors.New("this host sends no mail")
			} else {
				err = e.Mail.Send(ctx, []string{c.Email}, subject, body)
			}
		}
		if err != nil {
			e.logf("reward: mail %s for claim %s: %v", t.ID, c.Id(), err)
		}
	}
}

func render(t rule.Template, p *rule.Program, c *claim.Claim) (string, string, error) {
	data := map[string]any{
		"program": p.Name, "rule": c.Rule, "subject": c.Subject,
		"points": c.Points, "creditCents": c.CreditCents, "fields": c.Fields,
	}
	var out [2]string
	for i, src := range []string{t.Subject, t.Body} {
		tpl, err := template.New(t.ID).Option("missingkey=zero").Parse(src)
		if err != nil {
			return "", "", err
		}
		var b bytes.Buffer
		if err := tpl.Execute(&b, data); err != nil {
			return "", "", err
		}
		out[i] = strings.TrimSpace(b.String())
	}
	return out[0], out[1], nil
}
