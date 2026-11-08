package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

// Program reads the program slug, or ErrNotFound.
func (e *Engine) Program(ctx context.Context, slug string) (*rule.Program, error) {
	return load[rule.Program](ctx, e.DB, rule.ProgramID(slug))
}

// Programs lists every program, by slug.
func (e *Engine) Programs(ctx context.Context) ([]*rule.Program, error) {
	rows, err := orm.TypedQuery[rule.Program](e.DB).GetAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		p.Init(e.DB)
	}
	slices.SortFunc(rows, func(a, b *rule.Program) int { return strings.Compare(a.Slug(), b.Slug()) })
	return rows, nil
}

// who is the staff member behind a change.
func who(by string) (string, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return "", refuse.New("the person making the change is required")
	}
	return by, nil
}

// validate is the program's own validation and what only the engine knows: whether
// the host can grant the credit its rules name.
func (e *Engine) validate(p *rule.Program) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for i := range p.Rules {
		if err := e.check(p, &p.Rules[i]); err != nil {
			return err
		}
	}
	return nil
}

func marshal(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// fingerprint is a program's content, for telling a change from a save that changes
// nothing.
func fingerprint(p *rule.Program) string {
	return marshal(struct {
		Name   string
		Active bool
		B, U   any
		L, Bd  any
		T, R   any
	}{p.Name, p.Active, p.Budget, p.Upline, p.Levels, p.Badges, p.Templates, p.Rules})
}

// SaveProgram creates the program slug, with in.Rules, or changes the header of the
// one that exists: its name, whether it is active, its budget, upline, levels,
// badges and templates. A stored program keeps its rules whatever in.Rules says; they are
// changed one at a time by SaveRule. Saving what is already stored changes and
// records nothing. by is who made the change.
//
// A program is edited by people in admin, one at a time; the last save wins and
// every save is on the trail.
func (e *Engine) SaveProgram(ctx context.Context, slug string, in rule.Program, by string) (*rule.Program, error) {
	by, err := who(by)
	if err != nil {
		return nil, err
	}
	p, err := e.Program(ctx, slug)
	created := err == ErrNotFound
	if err != nil && !created {
		return nil, err
	}
	var before string
	if created {
		p = orm.New[rule.Program](e.DB)
		p.SetId(rule.ProgramID(slug))
		for _, r := range in.Rules {
			r.Version = 1
			p.Rules = append(p.Rules, r)
		}
	} else {
		before = fingerprint(p)
	}
	p.Name, p.Active, p.Budget, p.Upline = strings.TrimSpace(in.Name), in.Active, in.Budget, in.Upline
	p.Levels, p.Badges, p.Templates = in.Levels, in.Badges, in.Templates
	if err := e.validate(p); err != nil {
		return nil, err
	}
	if !created && before == fingerprint(p) {
		return p, nil
	}
	p.Version++
	verb := "program.updated"
	if created {
		verb = "program.created"
		now := e.now()
		p.CreatedAt, p.UpdatedAt = now, now
		ok, err := e.DB.CreateIfAbsent(ctx, p.Key(), p)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, refuse.New("program %s was created by someone else a moment ago", slug)
		}
	} else if err := p.PutCtx(ctx); err != nil {
		return nil, err
	}
	return p, e.record(ctx, by, verb, "program/"+slug, map[string]any{"version": p.Version, "rules": len(p.Rules)})
}

// SaveRule creates or replaces the rule r in the program slug. Its Version is the
// store's: 1 for a new rule and one more each time the rule changes, so a claim
// made under an earlier version still awards what that version said. Saving a rule
// as it already is changes and records nothing.
func (e *Engine) SaveRule(ctx context.Context, slug string, r rule.Rule, by string) (*rule.Program, error) {
	by, err := who(by)
	if err != nil {
		return nil, err
	}
	p, err := e.Program(ctx, slug)
	if err != nil {
		return nil, err
	}
	cur := p.Find(r.ID)
	r.Version = 1
	if cur != nil {
		r.Version = cur.Version
	}
	if err := e.check(p, &r); err != nil {
		return nil, err
	}
	verb := "rule.created"
	if cur != nil {
		if marshal(*cur) == marshal(r) {
			return p, nil
		}
		r.Version++
		*cur = r
		verb = "rule.updated"
	} else {
		p.Rules = append(p.Rules, r)
	}
	p.Version++
	if err := p.PutCtx(ctx); err != nil {
		return nil, err
	}
	return p, e.record(ctx, by, verb, "program/"+slug, map[string]any{"rule": r.ID, "ruleVersion": r.Version, "version": p.Version})
}

// used reports whether any claim or ledger row names the program, or one of its
// rules when ruleID is not empty. What they name is history and is not deleted.
func (e *Engine) used(ctx context.Context, slug, ruleID string) (bool, error) {
	q := orm.TypedQuery[claim.Claim](e.DB).Filter("Program=", slug)
	if ruleID != "" {
		q = q.Filter("Rule=", ruleID)
	}
	n, err := q.Limit(1).Count(ctx)
	if err != nil || n > 0 {
		return n > 0, err
	}
	l := orm.TypedQuery[ledger.Entry](e.DB).Filter("Program=", slug)
	if ruleID != "" {
		l = l.Filter("Rule=", ruleID)
	}
	n, err = l.Limit(1).Count(ctx)
	return n > 0, err
}

// DeleteRule removes a rule nothing has claimed. A rule that has claims is retired
// with active=false instead: the ledger names it.
func (e *Engine) DeleteRule(ctx context.Context, slug, ruleID, by string) (*rule.Program, error) {
	by, err := who(by)
	if err != nil {
		return nil, err
	}
	p, err := e.Program(ctx, slug)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(p.Rules, func(r rule.Rule) bool { return r.ID == ruleID })
	if i < 0 {
		return nil, ErrNotFound
	}
	if has, err := e.used(ctx, slug, ruleID); err != nil {
		return nil, err
	} else if has {
		return nil, refuse.New("rule %s has claims; retire it with active=false", ruleID)
	}
	p.Rules = slices.Delete(p.Rules, i, i+1)
	p.Version++
	if err := p.PutCtx(ctx); err != nil {
		return nil, err
	}
	return p, e.record(ctx, by, "rule.deleted", "program/"+slug, map[string]any{"rule": ruleID, "version": p.Version})
}

// DeleteProgram removes a program nothing has claimed under.
func (e *Engine) DeleteProgram(ctx context.Context, slug, by string) error {
	by, err := who(by)
	if err != nil {
		return err
	}
	p, err := e.Program(ctx, slug)
	if err != nil {
		return err
	}
	if has, err := e.used(ctx, slug, ""); err != nil {
		return err
	} else if has {
		return refuse.New("program %s has claims; retire it with active=false", slug)
	}
	if err := e.DB.Delete(ctx, p.Key()); err != nil {
		return fmt.Errorf("reward: delete program %s: %w", slug, err)
	}
	return e.record(ctx, by, "program.deleted", "program/"+slug, nil)
}

// Filter selects claims. Zero fields select everything.
type Filter struct {
	Status  claim.Status
	Program string
	Subject string
	// Limit is how many to return, at most 200; 0 is 50.
	Limit  int
	Offset int
}

// Claims lists claims, newest first.
func (e *Engine) Claims(ctx context.Context, f Filter) ([]*claim.Claim, error) {
	q := orm.TypedQuery[claim.Claim](e.DB)
	if f.Status != "" {
		q = q.Filter("Status=", string(f.Status))
	}
	if f.Program != "" {
		q = q.Filter("Program=", f.Program)
	}
	if f.Subject != "" {
		q = q.Filter("Subject=", f.Subject)
	}
	n := f.Limit
	if n <= 0 {
		n = 50
	}
	return q.Order("-createdAt").Offset(max(f.Offset, 0)).Limit(min(n, 200)).GetAll(ctx)
}

// Standing computes the subject's account in the program slug.
func (e *Engine) Standing(ctx context.Context, slug, subject string) (ledger.Account, error) {
	p, err := e.Program(ctx, slug)
	if err != nil {
		return ledger.Account{}, err
	}
	return ledger.Standing(ctx, e.DB, p, subject)
}
