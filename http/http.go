// Package http is the engine's HTTP face, native zip: handlers registered on the
// host's zip.App, one router and one middleware chain.
//
//	GET    /v1/reward/health                                     public
//	GET    /v1/reward/catalog                                    public
//	POST   /v1/reward/events                                     platform: ingest
//	GET    /v1/reward/me                                         the caller: balances, level, badges
//	GET    /v1/reward/me/claims                                  the caller: claims
//	GET    /v1/reward/me/explore                                 the caller: active rules not yet earned, by pillar
//	GET    /v1/reward/me/profile                                 the caller: their profile
//	PUT    /v1/reward/me/profile                                 the caller: handle, public, links
//	GET    /v1/reward/profile/:handle                            public, once the owner turns it on
//	GET    /v1/reward/badge/:handle                              public: <handle>.svg, ?theme=dark|light&style=flat|card
//	GET    /v1/admin/reward/programs                             SuperAdmin
//	POST   /v1/admin/reward/programs                             SuperAdmin
//	GET    /v1/admin/reward/programs/:program                    SuperAdmin
//	PUT    /v1/admin/reward/programs/:program                    SuperAdmin
//	DELETE /v1/admin/reward/programs/:program                    SuperAdmin
//	PUT    /v1/admin/reward/programs/:program/rules/:rule        SuperAdmin
//	DELETE /v1/admin/reward/programs/:program/rules/:rule        SuperAdmin
//	POST   /v1/admin/reward/programs/:program/backfill           SuperAdmin: legacy points
//	GET    /v1/admin/reward/claims                               SuperAdmin: the queue
//	POST   /v1/admin/reward/claims/:claim/approve                SuperAdmin
//	POST   /v1/admin/reward/claims/:claim/reject                 SuperAdmin
//	POST   /v1/admin/reward/claims/:claim/reverse                SuperAdmin
//	POST   /v1/admin/reward/simulate                             SuperAdmin: a dry run
//
// WHO IS CALLING is read from the identity headers the edge writes (authz.Header*)
// and nowhere else; this package holds no credential and checks no token. The edge
// strips those headers from every client request and restates them from a verified
// token, so behind it they cannot be forged. Two questions are asked of them:
//
//   - the platform principal: X-User-IsAdmin is "true", which is the edge's
//     SuperAdmin verdict (owner == "admin"). An org admin is not one. It gates the
//     admin routes and the ingest, which cloud calls in process as the platform.
//   - the subject: X-User-Id. The caller's own records are the ones keyed by it; no
//     route takes a subject from its path or body for a caller who is not the platform.
package http

import (
	"errors"
	nethttp "net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

// Prefixes of the two halves of the surface.
const (
	Prefix      = "/v1/reward"
	AdminPrefix = "/v1/admin/reward"
)

// Route registers every route on app.
func Route(app *zip.App, e *engine.Engine) {
	h := handlers{e}
	app.Raw(nethttp.MethodGet, Prefix+"/health", h.health)
	app.Raw(nethttp.MethodGet, Prefix+"/catalog", h.catalog)
	app.Raw(nethttp.MethodPost, Prefix+"/events", h.ingest)
	app.Raw(nethttp.MethodGet, Prefix+"/me", h.me)
	app.Raw(nethttp.MethodGet, Prefix+"/me/claims", h.myClaims)
	app.Raw(nethttp.MethodGet, Prefix+"/me/explore", h.explore)
	app.Raw(nethttp.MethodGet, Prefix+"/me/profile", h.myProfile)
	app.Raw(nethttp.MethodPut, Prefix+"/me/profile", h.saveMyProfile)
	app.Raw(nethttp.MethodGet, Prefix+"/profile/:handle", h.show)
	app.Raw(nethttp.MethodGet, Prefix+"/badge/:handle", h.emblem)

	app.Raw(nethttp.MethodGet, AdminPrefix+"/programs", h.programs)
	app.Raw(nethttp.MethodPost, AdminPrefix+"/programs", h.createProgram)
	app.Raw(nethttp.MethodGet, AdminPrefix+"/programs/:program", h.program)
	app.Raw(nethttp.MethodPut, AdminPrefix+"/programs/:program", h.saveProgram)
	app.Raw(nethttp.MethodDelete, AdminPrefix+"/programs/:program", h.deleteProgram)
	app.Raw(nethttp.MethodPut, AdminPrefix+"/programs/:program/rules/:rule", h.saveRule)
	app.Raw(nethttp.MethodDelete, AdminPrefix+"/programs/:program/rules/:rule", h.deleteRule)
	app.Raw(nethttp.MethodPost, AdminPrefix+"/programs/:program/backfill", h.backfill)
	app.Raw(nethttp.MethodGet, AdminPrefix+"/claims", h.claims)
	app.Raw(nethttp.MethodPost, AdminPrefix+"/claims/:claim/approve", h.approve)
	app.Raw(nethttp.MethodPost, AdminPrefix+"/claims/:claim/reject", h.reject)
	app.Raw(nethttp.MethodPost, AdminPrefix+"/claims/:claim/reverse", h.reverse)
	app.Raw(nethttp.MethodPost, AdminPrefix+"/simulate", h.simulate)
}

type handlers struct{ e *engine.Engine }

// caller is who the edge says is calling.
type caller struct {
	subject  string
	email    string
	platform bool
}

func who(c *zip.Ctx) caller {
	return caller{
		subject:  strings.TrimSpace(c.Header(authz.HeaderUser)),
		email:    strings.TrimSpace(c.Header(authz.HeaderUserEmail)),
		platform: c.Header(authz.HeaderUserAdmin) == "true",
	}
}

// actor names the person behind a staff act, for the claim and the trail.
func (w caller) actor() string {
	if w.email != "" {
		return w.email
	}
	return w.subject
}

// platform lets the platform principal through and answers everyone else: 401 for a
// caller the edge did not identify, 403 for one it did. It is checked inside every
// handler that needs it, never only on a group.
func platform(c *zip.Ctx) (caller, bool) {
	w := who(c)
	switch {
	case w.platform && w.subject != "":
		return w, true
	case w.subject == "":
		_ = fail(c, nethttp.StatusUnauthorized, "authentication required", nil)
	default:
		_ = fail(c, nethttp.StatusForbidden, "this operation requires platform-administrator credentials", nil)
	}
	return w, false
}

// member lets an identified caller through.
func member(c *zip.Ctx) (caller, bool) {
	w := who(c)
	if w.subject == "" {
		_ = fail(c, nethttp.StatusUnauthorized, "authentication required", nil)
		return w, false
	}
	return w, true
}

type problem struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func fail(c *zip.Ctx, status int, msg string, _ error) error {
	var p problem
	p.Error.Type, p.Error.Message = "api-error", msg
	return c.JSON(status, p)
}

// answer maps an engine error to the status it deserves.
func (h handlers) answer(c *zip.Ctx, err error) error {
	switch {
	case refuse.Is(err):
		return fail(c, nethttp.StatusBadRequest, err.Error(), err)
	case errors.Is(err, engine.ErrNotFound):
		return fail(c, nethttp.StatusNotFound, "not found", err)
	case errors.Is(err, engine.ErrState), errors.Is(err, engine.ErrTaken):
		return fail(c, nethttp.StatusConflict, err.Error(), err)
	}
	h.e.Logf("reward: %s %s: %v", c.Method(), c.Path(), err)
	return fail(c, nethttp.StatusInternalServerError, "internal error", err)
}

func (h handlers) health(c *zip.Ctx) error {
	return c.JSON(nethttp.StatusOK, map[string]string{"status": "ok", "service": "reward"})
}

// catalogDoc is the catalog and the vocabulary of a rule, for the admin UI's dropdowns.
type catalogDoc struct {
	Pillars []catalog.Group `json:"pillars"`
	rule.Words
}

// catalog is the typed list of events, grouped under their pillars, with the ops,
// action types, approvals and limits a rule is built from. Public, and the same for
// everyone.
func (h handlers) catalog(c *zip.Ctx) error {
	c.SetHeader("Cache-Control", "public, max-age=300")
	return c.JSON(nethttp.StatusOK, catalogDoc{Pillars: catalog.Lookup(), Words: rule.Vocabulary()})
}

// ingest evaluates one event: every active rule it satisfies makes a claim, inside
// its limits and the program's budget. The same event id again makes no second
// claim. The platform principal only: cloud calls it, as the platform, for the event
// it saw.
func (h handlers) ingest(c *zip.Ctx) error {
	if _, ok := platform(c); !ok {
		return nil
	}
	var ev catalog.Event
	if err := c.Bind(&ev); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	out, err := h.e.Evaluate(c.Context(), ev)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, out)
}

type mine struct {
	Subject  string           `json:"subject"`
	Programs []ledger.Account `json:"programs"`
}

// me answers the caller's own standing in every active program: lifetime and
// redeemable points, level, next level and badges. The subject is the caller's, from
// the edge, never from the request.
func (h handlers) me(c *zip.Ctx) error {
	w, ok := member(c)
	if !ok {
		return nil
	}
	progs, err := h.e.Programs(c.Context())
	if err != nil {
		return h.answer(c, err)
	}
	out := mine{Subject: w.subject, Programs: []ledger.Account{}}
	for _, p := range progs {
		if !p.Active {
			continue
		}
		a, err := ledger.Standing(c.Context(), h.e.DB, p, w.subject)
		if err != nil {
			return h.answer(c, err)
		}
		out.Programs = append(out.Programs, a)
	}
	return c.JSON(nethttp.StatusOK, out)
}

// myClaims lists the caller's own claims, newest first. ?status= narrows them.
func (h handlers) myClaims(c *zip.Ctx) error {
	w, ok := member(c)
	if !ok {
		return nil
	}
	f, err := filter(c)
	if err != nil {
		return h.answer(c, err)
	}
	f.Subject = w.subject
	rows, err := h.e.Claims(c.Context(), f)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"claims": rows})
}

func filter(c *zip.Ctx) (engine.Filter, error) {
	f := engine.Filter{Status: claim.Status(c.Query("status")), Program: c.Query("program")}
	if f.Status != "" && !f.Status.Valid() {
		return f, refuse.New("%q is not a status", f.Status)
	}
	for name, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if s := c.Query(name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				return f, refuse.New("%s must be a number from 0", name)
			}
			*dst = n
		}
	}
	return f, nil
}
