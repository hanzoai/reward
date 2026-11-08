package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
)

// Most rows one Backfill takes. A larger history is sent in several calls; each is
// idempotent, so the seams cost nothing.
const MaxBackfill = 5000

// Backfilled is what a backfill did.
type Backfilled struct {
	// Written rows were new; Skipped rows were already there and are untouched.
	Written int `json:"written"`
	Skipped int `json:"skipped"`
	// Points is what the written rows add to lifetime points.
	Points int64 `json:"points"`
}

// Backfill grandfathers in the points subjects earned before the ledger, as legacy
// rows of the program slug. It is one staff act: the whole call is a single record
// on the trail naming who ran it, however many rows it carries; it is not
// thousands of claims for staff to approve.
//
// Each (subject, source) is one row, written once, so running it again changes
// nothing, including with a different count: the first stands. Legacy points count
// toward lifetime points, levels and rank and never toward redeemable points. The
// rows are checked first and the call refuses all of them if one is wrong.
func (e *Engine) Backfill(ctx context.Context, slug string, rows []ledger.Past, by string) (Backfilled, error) {
	var out Backfilled
	by, err := who(by)
	if err != nil {
		return out, err
	}
	if _, err := e.Program(ctx, slug); err != nil {
		return out, err
	}
	if len(rows) == 0 || len(rows) > MaxBackfill {
		return out, refuse.New("a backfill takes from 1 to %d rows", MaxBackfill)
	}
	for i, r := range rows {
		r.Subject, r.Source = strings.TrimSpace(r.Subject), strings.TrimSpace(r.Source)
		switch {
		case r.Subject == "" || len(r.Subject) > 256 || strings.ContainsAny(r.Subject, "\x00\n\r"):
			return out, refuse.New("row %d: a subject of 1 to 256 characters on one line is required", i)
		case !slices.Contains(ledger.Sources, r.Source):
			return out, refuse.New("row %d: %q is not a source (%s)", i, r.Source, strings.Join(ledger.Sources, ", "))
		case r.Points < 1:
			return out, refuse.New("row %d: points must be at least 1", i)
		case len(r.Evidence) > 1024:
			return out, refuse.New("row %d: evidence is at most 1024 characters", i)
		}
		rows[i] = r
	}
	now := e.now()
	for _, r := range rows {
		x := orm.New[ledger.Entry](e.DB)
		x.SetId(ledger.LegacyID(slug, r.Subject, r.Source))
		x.Program, x.Subject, x.Rule = slug, r.Subject, "legacy/"+r.Source
		x.Act, x.Points, x.Redeemable = ledger.Legacy, r.Points, false
		x.Source, x.Evidence, x.By, x.At = r.Source, r.Evidence, by, now
		x.CreatedAt, x.UpdatedAt = now, now
		created, err := e.DB.CreateIfAbsent(ctx, x.Key(), x)
		if err != nil {
			return out, fmt.Errorf("reward: backfill %s/%s: %w", r.Subject, r.Source, err)
		}
		if created {
			out.Written++
			out.Points += r.Points
		} else {
			out.Skipped++
		}
	}
	return out, e.record(ctx, by, "ledger.backfilled", "program/"+slug, map[string]any{
		"rows": len(rows), "written": out.Written, "skipped": out.Skipped, "points": out.Points,
	})
}
