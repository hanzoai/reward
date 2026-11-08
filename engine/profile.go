package engine

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/profile"
	"github.com/hanzoai/reward/rule"
)

// ErrTaken is a handle someone else holds.
var ErrTaken = errors.New("reward: that handle is taken")

// Profile reads the subject's own profile. A subject who has never saved one has the
// empty, private profile.
func (e *Engine) Profile(ctx context.Context, subject string) (*profile.Profile, error) {
	p, err := load[profile.Profile](ctx, e.DB, profile.ProfileID(subject))
	if errors.Is(err, ErrNotFound) {
		p = orm.New[profile.Profile](e.DB)
		p.SetId(profile.ProfileID(subject))
		return p, nil
	}
	return p, err
}

// SaveProfile sets the subject's handle, whether their standing is public, and their
// links. The handle is taken by a conditional insert, so two subjects choosing it at
// once cannot both have it. Only the subject writes their own profile; nothing here
// reads a subject from a request.
func (e *Engine) SaveProfile(ctx context.Context, subject string, in profile.Profile) (*profile.Profile, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	cur, err := e.Profile(ctx, subject)
	if err != nil {
		return nil, err
	}
	if in.Handle != "" && in.Handle != cur.Handle {
		h := orm.New[profile.Handle](e.DB)
		h.SetId(profile.HandleID(in.Handle))
		h.Subject = subject
		now := e.now()
		h.CreatedAt, h.UpdatedAt = now, now
		created, err := e.DB.CreateIfAbsent(ctx, h.Key(), h)
		if err != nil {
			return nil, err
		}
		if !created {
			held, err := load[profile.Handle](ctx, e.DB, profile.HandleID(in.Handle))
			if err != nil {
				return nil, err
			}
			if held.Subject != subject {
				return nil, ErrTaken
			}
		}
	}
	old := cur.Handle
	cur.Handle, cur.Public, cur.Links = in.Handle, in.Public, in.Links
	if err := cur.PutCtx(ctx); err != nil {
		return nil, err
	}
	if old != "" && old != in.Handle {
		if h, err := load[profile.Handle](ctx, e.DB, profile.HandleID(old)); err == nil && h.Subject == subject {
			if err := e.DB.Delete(ctx, h.Key()); err != nil {
				return cur, err
			}
		}
	}
	return cur, nil
}

// Face is one badge as the public sees it.
type Face struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Pillar   catalog.Pillar `json:"pillar"`
	Rarity   rule.Rarity    `json:"rarity"`
	EarnedAt string         `json:"earnedAt"`
}

// Public is a subject's public profile: what they chose to show and nothing else.
// There is no email in it, no redeemable balance and no subject id.
type Public struct {
	Handle    string         `json:"handle"`
	Level     int            `json:"level"`
	LevelName string         `json:"levelName"`
	Band      string         `json:"band"`
	Points    int64          `json:"points"`
	Badges    []Face         `json:"badges"`
	Pillars   map[string]int `json:"pillars"`
	Links     profile.Links  `json:"links"`
}

// Show reads the public profile at handle in the program slug, or in the first
// active program when slug is empty. A handle nobody holds, and a profile whose owner
// has not made it public, are the same ErrNotFound: the answer does not say which.
func (e *Engine) Show(ctx context.Context, handle, slug string) (Public, error) {
	var out Public
	h, err := load[profile.Handle](ctx, e.DB, profile.HandleID(profile.Normalize(handle)))
	if err != nil {
		return out, err
	}
	p, err := load[profile.Profile](ctx, e.DB, profile.ProfileID(h.Subject))
	if err != nil {
		return out, err
	}
	if !p.Public || profile.HandleID(p.Handle) != h.Id() {
		return out, ErrNotFound
	}
	prog, err := e.shown(ctx, slug)
	if err != nil {
		return out, err
	}
	a, err := ledger.Standing(ctx, e.DB, prog, h.Subject)
	if err != nil {
		return out, err
	}
	counts, err := e.counts(ctx, h.Subject, prog.Slug())
	if err != nil {
		return out, err
	}
	out = Public{Handle: p.Handle, Level: a.Level.Level, LevelName: a.Level.Name, Band: a.Band, Points: a.Lifetime, Pillars: counts, Links: p.Links, Badges: []Face{}}
	for _, b := range a.Badges {
		out.Badges = append(out.Badges, Face{ID: b.ID, Name: b.Name, Pillar: b.Pillar, Rarity: b.Rarity, EarnedAt: b.EarnedAt.Format("2006-01-02T15:04:05Z")})
	}
	return out, nil
}

// Top is the n badges a profile shows first: the rarest, then the most recently earned.
func (p Public) Top(n int) []Face {
	faces := slices.Clone(p.Badges)
	slices.SortFunc(faces, func(a, b Face) int {
		return cmp.Or(
			cmp.Compare(b.Rarity.Rank(), a.Rarity.Rank()),
			cmp.Compare(b.EarnedAt, a.EarnedAt),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return faces[:min(n, len(faces))]
}

// tiers are the ids of the token milestone badges, lowest first: the seed's
// usage.tokens rules award them at 100K, 1M, 5M, 10M, 100M, 1B, 10B, 100B and 1T tokens.
var tiers = []string{"first-100k", "million-token-club", "five-million", "ten-million", "century", "billionaire", "titan", "colossus", "trillion-club"}

// Tier is the name of the highest token milestone badge the profile holds, or empty
// when it holds none. It is the badge's name, never a count of tokens.
func (p Public) Tier() string {
	for _, id := range slices.Backward(tiers) {
		for _, f := range p.Badges {
			if f.ID == id {
				return f.Name
			}
		}
	}
	return ""
}

// shown is the program a public profile is read in.
func (e *Engine) shown(ctx context.Context, slug string) (*rule.Program, error) {
	if slug != "" {
		return e.Program(ctx, slug)
	}
	progs, err := e.Programs(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range progs {
		if p.Active {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

// counts is how many claims of each pillar the subject has had awarded.
func (e *Engine) counts(ctx context.Context, subject, program string) (map[string]int, error) {
	out := map[string]int{}
	for _, p := range catalog.Pillars() {
		out[string(p.ID)] = 0
	}
	rows, err := orm.TypedQuery[claim.Claim](e.DB).Filter("Subject=", subject).Filter("Status=", string(claim.Awarded)).GetAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range rows {
		if c.Program == program {
			out[string(c.Pillar)]++
		}
	}
	return out, nil
}

// Prospect is an active rule the subject has not yet earned: what to do next.
type Prospect struct {
	Program  string            `json:"program"`
	Rule     string            `json:"rule"`
	Name     string            `json:"name"`
	Event    catalog.EventType `json:"event"`
	Points   int64             `json:"points"`
	Badges   []rule.Badge      `json:"badges"`
	Approval rule.Approval     `json:"approval"`
}

// Ahead is the prospects under one pillar.
type Ahead struct {
	catalog.PillarInfo
	Rules []Prospect `json:"rules"`
}

// Explore lists, by pillar in the catalog's order, the active rules of the active
// programs that the subject has no live claim under yet, each with the points and
// badges it gives and the catalog event that earns it. It is computed from the
// programs, the catalog and the subject's claims and invents nothing: a rule with
// nothing to give still shows what it is.
func (e *Engine) Explore(ctx context.Context, subject string) ([]Ahead, error) {
	progs, err := e.Programs(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := orm.TypedQuery[claim.Claim](e.DB).Filter("Subject=", subject).GetAll(ctx)
	if err != nil {
		return nil, err
	}
	done := map[[2]string]bool{}
	for _, c := range rows {
		if c.Status.Live() {
			done[[2]string{c.Program, c.Rule}] = true
		}
	}
	out := make([]Ahead, 0, 5)
	at := map[catalog.Pillar]int{}
	for _, p := range catalog.Pillars() {
		at[p.ID] = len(out)
		out = append(out, Ahead{PillarInfo: p, Rules: []Prospect{}})
	}
	for _, p := range progs {
		if !p.Active {
			continue
		}
		for _, r := range p.Rules {
			if !r.Active || done[[2]string{p.Slug(), r.ID}] {
				continue
			}
			ev, ok := catalog.Type(r.When.Event)
			if !ok {
				continue
			}
			pr := Prospect{Program: p.Slug(), Rule: r.ID, Name: r.Name, Event: ev, Approval: r.Approval, Badges: []rule.Badge{}}
			for _, a := range r.Then {
				switch a.Type {
				case rule.Do.Points:
					pr.Points += a.Points
				case rule.Do.Badge:
					if b := p.Badge(a.Badge); b != nil {
						pr.Badges = append(pr.Badges, *b)
					}
				}
			}
			i := at[r.Pillar]
			out[i].Rules = append(out[i].Rules, pr)
		}
	}
	return out, nil
}
