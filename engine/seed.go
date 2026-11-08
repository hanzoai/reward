package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/rule"
)

// seedJSON is the program a fresh store starts with: the owner-approved earn
// table, levels and caps as active rules, and the bounty, product, model and
// expert rules as inactive ones at 0 points, for staff to price in admin. Every
// rule is manual.
//
//go:embed seed/rewards.json
var seedJSON []byte

// seedBy is who the trail names for the seed.
const seedBy = "seed"

// Seed creates the initial program from the embedded document, and only that: a
// program that already exists is left exactly as it is, however far admin has
// changed it, and nothing here ever overwrites it. It reports whether it created
// the program.
func (e *Engine) Seed(ctx context.Context) (bool, error) {
	var in struct {
		ID string `json:"id"`
		rule.Program
	}
	if err := json.Unmarshal(seedJSON, &in); err != nil {
		return false, fmt.Errorf("reward: the embedded seed: %w", err)
	}
	if _, err := e.Program(ctx, in.ID); err == nil {
		return false, nil
	} else if err != ErrNotFound {
		return false, err
	}
	p := orm.New[rule.Program](e.DB)
	p.SetId(rule.ProgramID(in.ID))
	p.Name, p.Active, p.Budget, p.Upline = in.Name, in.Active, in.Budget, in.Upline
	p.Levels, p.Badges, p.Templates, p.Version = in.Levels, in.Badges, in.Templates, 1
	for _, r := range in.Rules {
		r.Version = 1
		p.Rules = append(p.Rules, r)
	}
	if err := e.validate(p); err != nil {
		return false, fmt.Errorf("reward: the embedded seed is not valid: %w", err)
	}
	now := e.now()
	p.CreatedAt, p.UpdatedAt = now, now
	created, err := e.DB.CreateIfAbsent(ctx, p.Key(), p)
	if err != nil || !created {
		// Another replica seeded between the read and the write.
		return false, err
	}
	return true, e.record(ctx, seedBy, "program.created", "program/"+in.ID, map[string]any{"version": 1, "rules": len(p.Rules)})
}
