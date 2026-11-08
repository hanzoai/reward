package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/rule"
)

// seedJSON is the program a fresh store starts with: the owner-approved earn
// table, levels and caps as active rules, the owner's later tenure, streak, founder
// and referral rules, and the bounty, product, model and expert rules as inactive
// ones at 0 points, for staff to price in admin. Every rule is manual. Its
// seedVersion is the one a program is brought up to.
//
//go:embed seed/rewards.json
var seedJSON []byte

// seedBy is who the trail names for the seed.
const seedBy = "seed"

// firstLevels is the levels table the first seed wrote (v0.1.4 and before; seedVersion
// 0). A stored program whose table is still exactly this one has never had its levels
// edited, so a top-up may replace it with the seed's; a table that differs in any
// field is staff's and is left alone.
var firstLevels = []rule.Level{
	{Level: 1, Name: "Hacker"},
	{Level: 2, Name: "Builder", MinLifetimePoints: 100},
	{Level: 3, Name: "Shipper", MinLifetimePoints: 500},
	{Level: 4, Name: "Core", MinLifetimePoints: 2500},
	{Level: 5, Name: "Legend", MinLifetimePoints: 10000},
}

// Seeded is what one Seed did.
type Seeded struct {
	// Created is true when the store had no program and Seed made it.
	Created bool
	// Rules and Badges are how many a top-up appended; Levels and Upline say whether
	// it replaced those with the seed's.
	Rules, Badges  int
	Levels, Upline bool
}

// Seed creates the initial program from the embedded document when the store has
// none. A program that exists is never overwritten, however far admin has changed
// it; one created by an earlier seed (its SeedVersion is below the embedded one) is
// topped up instead, see topUp.
func (e *Engine) Seed(ctx context.Context) (Seeded, error) { return e.seed(ctx, seedJSON) }

func (e *Engine) seed(ctx context.Context, raw []byte) (Seeded, error) {
	var in struct {
		ID string `json:"id"`
		rule.Program
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Seeded{}, fmt.Errorf("reward: the embedded seed: %w", err)
	}
	cur, err := e.Program(ctx, in.ID)
	if err == nil {
		return e.topUp(ctx, cur, in.Program)
	} else if err != ErrNotFound {
		return Seeded{}, err
	}
	p := orm.New[rule.Program](e.DB)
	p.SetId(rule.ProgramID(in.ID))
	// FoundingCutoff is not seeded: the owner sets the date in admin.
	p.Name, p.Active, p.Budget, p.Upline = in.Name, in.Active, in.Budget, in.Upline
	p.Levels, p.Badges, p.Templates, p.Version, p.SeedVersion = in.Levels, in.Badges, in.Templates, 1, in.SeedVersion
	for _, r := range in.Rules {
		r.Version = 1
		p.Rules = append(p.Rules, r)
	}
	if err := e.validate(p); err != nil {
		return Seeded{}, fmt.Errorf("reward: the embedded seed is not valid: %w", err)
	}
	now := e.now()
	p.CreatedAt, p.UpdatedAt = now, now
	created, err := e.DB.CreateIfAbsent(ctx, p.Key(), p)
	if err != nil || !created {
		// Another replica seeded between the read and the write.
		return Seeded{}, err
	}
	return Seeded{Created: true}, e.record(ctx, seedBy, "program.created", "program/"+in.ID, map[string]any{"version": 1, "rules": len(p.Rules)})
}

// topUp brings a program created by an earlier seed up to the embedded one, once:
//
//   - every seed rule and badge whose id the program lacks is appended, as seeded
//     (rules at version 1). A rule or badge that exists is never touched, whatever
//     its state: staff own it. A rule staff deleted comes back if its id is in the
//     seed, because an absent id cannot be told from one never added.
//   - the levels table is replaced only if it is still firstLevels.
//   - the upline is set from the seed only if it is empty; a stored one stays.
//
// The program's Version rises, SeedVersion becomes the seed's, and one program.seeded
// trail record names what was added. A program already at the seed's version is left
// as it is. It is read, changed and written in one pass, and ids are checked against
// the document being written, so two replicas mounting together each write a whole
// document with every id once; a top-up that fails before its write leaves
// SeedVersion behind, and the next mount does it again.
func (e *Engine) topUp(ctx context.Context, p *rule.Program, seed rule.Program) (Seeded, error) {
	var out Seeded
	if p.SeedVersion >= seed.SeedVersion {
		return out, nil
	}
	for _, r := range seed.Rules {
		if p.Find(r.ID) == nil {
			r.Version = 1
			p.Rules = append(p.Rules, r)
			out.Rules++
		}
	}
	for _, b := range seed.Badges {
		if p.Badge(b.ID) == nil {
			p.Badges = append(p.Badges, b)
			out.Badges++
		}
	}
	if slices.Equal(p.Levels, firstLevels) {
		p.Levels, out.Levels = seed.Levels, true
	}
	if len(p.Upline) == 0 && len(seed.Upline) > 0 {
		p.Upline, out.Upline = seed.Upline, true
	}
	p.SeedVersion = seed.SeedVersion
	if err := e.validate(p); err != nil {
		return Seeded{}, fmt.Errorf("reward: the seed top-up of %s is not valid: %w", p.Slug(), err)
	}
	p.Version++
	if err := p.PutCtx(ctx); err != nil {
		return Seeded{}, err
	}
	return out, e.record(ctx, seedBy, "program.seeded", "program/"+p.Slug(), map[string]any{
		"seedVersion": p.SeedVersion, "version": p.Version, "rules": out.Rules, "badges": out.Badges, "levels": out.Levels, "upline": out.Upline})
}
