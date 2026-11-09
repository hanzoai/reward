// Package reward is the rewards rules engine as a module a host mounts: programs of
// rules, claims that staff verify, an append-only points ledger, levels, badges,
// budgets and caps. admin.hanzo.ai configures it; cloud feeds it events and mounts
// it as the reward capability at /v1/reward.
//
// The layers, each importing only those above it:
//
//	refuse   the error for input the engine will not take
//	catalog  the typed events a rule may read, under five pillars
//	rule     rules and the program that holds them
//	claim    one subject's claim to one rule's award, and the steps that decide it
//	ledger   the append-only points ledger and the trail of staff acts
//	sponsor  who brought a member in, one link per member
//	engine   Evaluate, Approve, Reject, Reverse, and the program's configuration
//	credit   the client for commerce's one credit path
//	http     the routes
package reward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/http"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/rule"
	"github.com/hanzoai/reward/sponsor"
)

// Config is what a host gives the module.
type Config struct {
	// DB is the store: the shared SQL through orm, or SQLite for local work. The
	// module keeps its rows in kinds of its own and touches no others.
	DB orm.DB
	// Credits grants credit for a rule's credit action. Nil refuses any rule that
	// has one. package credit is the client for commerce's POST /v1/billing/credit.
	Credits engine.Credits
	// Mail delivers an email action's message. Nil sends none, and says so in the log.
	Mail engine.Mailer
	// Log is the host's logger; nil is slog.Default().
	Log *slog.Logger
}

// Mount registers the reward capability on app, per the host-mount contract
// commerce follows: it registers handlers directly on the host's zip.App, so there
// is one router and one middleware chain, and it does not name its host. It builds
// the store's indexes, creates the initial program if the store has none (after that
// admin owns it; a program an earlier seed made is only topped up, see engine.Seed),
// and returns the engine so the host can call
// Evaluate, Approve and the rest in process.
func Mount(app *zip.App, cfg Config) (*engine.Engine, error) {
	if app == nil {
		return nil, errors.New("reward.Mount: nil zip.App")
	}
	if cfg.DB == nil {
		return nil, errors.New("reward.Mount: no store")
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	if err := orm.Ready(cfg.DB, rule.Kind, claim.Kind, claim.StepKind, ledger.Kind, ledger.TrailKind, sponsor.Kind); err != nil {
		return nil, fmt.Errorf("reward.Mount: %w", err)
	}
	e := &engine.Engine{
		DB: cfg.DB, Credits: cfg.Credits, Mail: cfg.Mail,
		Log: func(format string, args ...any) { log.Error(fmt.Sprintf(format, args...)) },
	}
	seeded, err := e.Seed(context.Background())
	if err != nil {
		return nil, fmt.Errorf("reward.Mount: seed: %w", err)
	}
	http.Route(app, e)
	log.Info("reward mounted", "prefix", http.Prefix, "admin", http.AdminPrefix,
		"seeded", seeded.Created, "rulesAdded", seeded.Rules, "badgesAdded", seeded.Badges, "levelsReplaced", seeded.Levels, "uplineSet", seeded.Upline)
	return e, nil
}
