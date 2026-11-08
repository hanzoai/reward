package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
	"github.com/hanzoai/reward/sponsor"
)

// maxClimb is how far up the sponsor chain a new link looks for the member it would
// close a cycle on.
const maxClimb = 64

// maxNetwork is the most members Network counts at one level. The counts are for
// display, so a level that reaches it says no more.
const maxNetwork = 10_000

// sponsorOf is who brought subject in, or "" for a member nobody brought.
func (e *Engine) sponsorOf(ctx context.Context, subject string) (string, error) {
	l, err := load[sponsor.Link](ctx, e.DB, sponsor.ID(subject))
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return l.Sponsor, nil
}

// link attributes the subject of a referral.linked event to the sponsor it names.
// A member has one sponsor, the first written: a second event for a member who has
// one changes nothing and is not an error. A member cannot sponsor themselves, nor
// a sponsor one of their own sponsors, however far up (a cycle).
func (e *Engine) link(ctx context.Context, ev catalog.Event) error {
	s, _ := ev.Fields["sponsor"].(string)
	s = strings.TrimSpace(s)
	switch {
	case s == "" || len(s) > 256 || strings.ContainsAny(s, "\x00\n\r"):
		return refuse.New("%s needs a sponsor of 1 to 256 characters on one line", ev.Type)
	case s == ev.Subject:
		return refuse.New("%s cannot sponsor themselves", s)
	}
	// The check and the write are one step in this process, under the same mutex as
	// a limit's: two replicas could still link a cycle at the same instant. Staff see
	// it in the rows (each link names the event that made it, and the upline rows it
	// pays are in the ledger), and an award climbs only UplineDepth links, so it ends.
	reserve.Lock()
	defer reserve.Unlock()
	if cur, err := e.sponsorOf(ctx, ev.Subject); err != nil || cur != "" {
		// A member keeps the sponsor they have; the event is otherwise an ordinary one.
		return err
	}
	for at, i := s, 0; i < maxClimb; i++ {
		up, err := e.sponsorOf(ctx, at)
		if err != nil {
			return err
		}
		if up == "" {
			break
		}
		if up == ev.Subject {
			return refuse.New("%s cannot sponsor %s: %s is already one of their sponsors", s, ev.Subject, ev.Subject)
		}
		at = up
	}
	l := orm.New[sponsor.Link](e.DB)
	l.SetId(sponsor.ID(ev.Subject))
	l.Subject, l.Sponsor, l.Event = ev.Subject, s, ev.ID
	l.CreatedAt = e.now()
	l.UpdatedAt = l.CreatedAt
	if _, err := e.DB.CreateIfAbsent(ctx, l.Key(), l); err != nil {
		return fmt.Errorf("reward: link %s to %s: %w", ev.Subject, s, err)
	}
	return nil
}

// pay writes the upline rows of an awarded claim: for each level the program names,
// the share of the claim's points that level's percentage gives the sponsor at that
// depth above the claim's subject, as points that are never redeemable. The chain is
// read now; links never change, so a retry reads the same one and rewrites the same
// rows. A share that rounds down to 0 writes nothing.
func (e *Engine) pay(ctx context.Context, c *claim.Claim) error {
	if c.Points <= 0 {
		return nil
	}
	p, err := e.Program(ctx, c.Program)
	if err != nil {
		return err
	}
	at := c.Subject
	for i, pct := range p.Upline {
		up, err := e.sponsorOf(ctx, at)
		if err != nil {
			return err
		}
		if up == "" {
			return nil
		}
		at = up
		share := c.Points * int64(pct) / 100
		if share == 0 {
			continue
		}
		x := orm.New[ledger.Entry](e.DB)
		x.Act, x.Subject, x.Points, x.Level = ledger.Upline, up, share, i+1
		if err := e.put(ctx, c, fmt.Sprint(i+1), x); err != nil {
			return err
		}
	}
	return nil
}

// Network is a member's sponsorship tree as they see it: how many members sit one,
// two and three levels below them, and the points the upline paid them in a program.
type Network struct {
	Level1 int   `json:"level1"`
	Level2 int   `json:"level2"`
	Level3 int   `json:"level3"`
	Points int64 `json:"points"`
}

// Network reads the subject's network in the program slug. Each level is the members
// whose sponsor is a member of the level above; a level stops at maxNetwork. Points
// are their upline rows less the reversals of them.
func (e *Engine) Network(ctx context.Context, slug, subject string) (Network, error) {
	var depth [rule.UplineDepth]int
	level := []string{subject}
	for i := range depth {
		var next []string
		for _, s := range level {
			room := maxNetwork - len(next)
			if room <= 0 {
				break
			}
			links, err := orm.TypedQuery[sponsor.Link](e.DB).Filter("Sponsor=", s).Limit(room).GetAll(ctx)
			if err != nil {
				return Network{}, err
			}
			for _, l := range links {
				next = append(next, l.Subject)
			}
		}
		depth[i], level = len(next), next
	}
	rows, err := ledger.Rows(ctx, e.DB, slug, subject)
	if err != nil {
		return Network{}, err
	}
	n := Network{Level1: depth[0], Level2: depth[1], Level3: depth[2]}
	for _, x := range rows {
		if x.Level > 0 {
			n.Points += x.Points
		}
	}
	return n, nil
}
