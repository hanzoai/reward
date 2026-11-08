package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/rule"
)

// The rig: a real store (SQLite through orm, a file per test) and a host credit
// book that dedups on the grant key the way commerce's ledger does. Exactly-once is
// a property of keys meeting a store, so only a real store can prove it.

type bank struct {
	mu      sync.Mutex
	grants  map[string]Grant
	calls   int
	broken  bool
	ceiling int64
}

func (b *bank) Ceiling() int64 { return b.ceiling }

func (b *bank) Grant(_ context.Context, g Grant) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.broken {
		return fmt.Errorf("ledger unavailable")
	}
	if _, ok := b.grants[g.Key]; !ok {
		b.grants[g.Key] = g
	}
	return nil
}

func (b *bank) total(org string) (n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range b.grants {
		if g.Org == org {
			n += g.Cents
		}
	}
	return n
}

type outbox struct {
	mu   sync.Mutex
	sent [][3]string
}

func (o *outbox) Send(_ context.Context, to []string, subject, body string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, [3]string{to[0], subject, body})
	return nil
}

type rig struct {
	t    *testing.T
	ctx  context.Context
	e    *Engine
	bank *bank
	mail *outbox
	slug string
	now  time.Time
}

func openDB(t *testing.T) orm.DB {
	t.Helper()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{
		Path:   filepath.Join(t.TempDir(), "reward.db"),
		Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newRig stands up a store, a program and a clock the test can move.
func newRig(t *testing.T, mod func(*rule.Program), rules ...rule.Rule) *rig {
	t.Helper()
	b := &bank{grants: map[string]Grant{}, ceiling: 100_000}
	m := &outbox{}
	r := &rig{t: t, ctx: context.Background(), bank: b, mail: m, slug: "t"}
	r.now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	r.e = &Engine{DB: openDB(t), Credits: b, Mail: m, Now: func() time.Time { return r.now },
		Log: func(f string, a ...any) { t.Logf(f, a...) }}

	p := rule.Program{
		Name: "Test", Active: true,
		Levels:    []rule.Level{{Level: 1, Name: "Hacker"}, {Level: 2, Name: "Builder", MinLifetimePoints: 100}, {Level: 3, Name: "Shipper", MinLifetimePoints: 500}},
		Badges:    []rule.Badge{{ID: "b1", Name: "One", Pillar: catalog.Use}, {ID: "b2", Name: "Two", Pillar: catalog.Contribute}},
		Templates: []rule.Template{{ID: "hello", Subject: "Hi {{.subject}}", Body: "You earned {{.points}} points in {{.program}} for {{.rule}}."}},
		Rules:     rules,
	}
	if mod != nil {
		mod(&p)
	}
	if _, err := r.e.SaveProgram(r.ctx, r.slug, p, "tester"); err != nil {
		t.Fatalf("save program: %v", err)
	}
	return r
}

// star is a rule on github.star.
func star(id string, then ...rule.Action) rule.Rule {
	return rule.Rule{ID: id, Name: id, Active: true, When: rule.When{Event: "github.star"}, Then: then}
}

func pts(n int64, redeemable bool) rule.Action {
	return rule.Action{Type: rule.Do.Points, Points: n, Redeemable: redeemable}
}

func badge(id string) rule.Action { return rule.Action{Type: rule.Do.Badge, Badge: id} }

func cash(cents int64) rule.Action { return rule.Action{Type: rule.Do.Credit, Cents: cents} }

func (r *rig) starEvent(id, subject, repo string) catalog.Event {
	return catalog.Event{ID: id, Type: "github.star", Subject: subject, Fields: map[string]any{"repo": repo}, Evidence: &catalog.Evidence{Source: "github", URL: "https://github.com/hanzoai/x", NeedsReview: true}}
}

func (r *rig) eval(ev catalog.Event) Outcome {
	r.t.Helper()
	out, err := r.e.Evaluate(r.ctx, ev)
	if err != nil {
		r.t.Fatalf("evaluate %s: %v", ev.ID, err)
	}
	return out
}

// decision is the one decision for the rule, failing if the event reached none.
func (r *rig) decision(out Outcome, ruleID string) Decision {
	r.t.Helper()
	for _, d := range out.Decisions {
		if d.Rule == ruleID {
			return d
		}
	}
	r.t.Fatalf("no decision for rule %s in %+v", ruleID, out.Decisions)
	return Decision{}
}

func (r *rig) claims(subject string) []*claim.Claim {
	r.t.Helper()
	all, err := r.e.Claims(r.ctx, Filter{Subject: subject, Limit: 200})
	if err != nil {
		r.t.Fatal(err)
	}
	return all
}

func (r *rig) account(subject string) ledger.Account {
	r.t.Helper()
	a, err := r.e.Standing(r.ctx, r.slug, subject)
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

func (r *rig) rows(subject string) []*ledger.Entry {
	r.t.Helper()
	rows, err := ledger.Rows(r.ctx, r.e.DB, r.slug, subject)
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

// trail counts the trail rows of a verb about an object.
func (r *rig) trail(verb, object string) int {
	r.t.Helper()
	rows, err := ledger.Acts(r.ctx, r.e.DB, verb, object)
	if err != nil {
		r.t.Fatal(err)
	}
	return len(rows)
}
