package http

import (
	nethttp "net/http"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/ledger"
	"github.com/hanzoai/reward/refuse"
	"github.com/hanzoai/reward/rule"
)

// header is a program without its rules: the part a program PUT changes.
type header struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Active    bool            `json:"active"`
	Budget    rule.Budget     `json:"budget"`
	Upline    []int           `json:"upline"`
	Levels    []rule.Level    `json:"levels"`
	Badges    []rule.Badge    `json:"badges"`
	Templates []rule.Template `json:"templates"`
	Rules     []rule.Rule     `json:"rules"`
}

func (b header) program() rule.Program {
	var p rule.Program
	p.Name, p.Active, p.Budget, p.Upline, p.Levels, p.Badges, p.Templates, p.Rules = b.Name, b.Active, b.Budget, b.Upline, b.Levels, b.Badges, b.Templates, b.Rules
	return p
}

// programs lists every program with its rules. SuperAdmin only.
func (h handlers) programs(c *zip.Ctx) error {
	if _, ok := platform(c); !ok {
		return nil
	}
	rows, err := h.e.Programs(c.Context())
	if err != nil {
		return h.answer(c, err)
	}
	views := make([]rule.View, 0, len(rows))
	for _, p := range rows {
		views = append(views, p.View())
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"programs": views})
}

// program reads one program with its rules. SuperAdmin only.
func (h handlers) program(c *zip.Ctx) error {
	if _, ok := platform(c); !ok {
		return nil
	}
	p, err := h.e.Program(c.Context(), c.Param("program"))
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, p.View())
}

// createProgram creates a program, with its rules if the body carries any; 409 when
// the id is taken. SuperAdmin only.
func (h handlers) createProgram(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	var in header
	if err := c.Bind(&in); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	if _, err := h.e.Program(c.Context(), in.ID); err == nil {
		return fail(c, nethttp.StatusConflict, "program "+in.ID+" exists; PUT changes it", nil)
	} else if err != engine.ErrNotFound {
		return h.answer(c, err)
	}
	p, err := h.e.SaveProgram(c.Context(), in.ID, in.program(), w.actor())
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusCreated, p.View())
}

// saveProgram changes a program's name, whether it is active, its budget, upline,
// levels, badges and templates. Its rules are kept as stored; they change one at a time at
// .../rules/:rule. SuperAdmin only.
func (h handlers) saveProgram(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	slug := c.Param("program")
	if _, err := h.e.Program(c.Context(), slug); err != nil {
		return h.answer(c, err)
	}
	var in header
	if err := c.Bind(&in); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	p, err := h.e.SaveProgram(c.Context(), slug, in.program(), w.actor())
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, p.View())
}

// deleteProgram removes a program nothing has claimed under; one that has claims is
// retired with active=false instead. SuperAdmin only.
func (h handlers) deleteProgram(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	if err := h.e.DeleteProgram(c.Context(), c.Param("program"), w.actor()); err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, map[string]bool{"deleted": true})
}

// saveRule creates or replaces one rule of a program. The version is the store's:
// it rises each time the rule changes, and a claim keeps the version it was made
// under. SuperAdmin only.
func (h handlers) saveRule(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	var r rule.Rule
	if err := c.Bind(&r); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	if r.ID == "" {
		r.ID = c.Param("rule")
	}
	if r.ID != c.Param("rule") {
		return h.answer(c, refuse.New("the rule id %q is not the one in the path, %q", r.ID, c.Param("rule")))
	}
	p, err := h.e.SaveRule(c.Context(), c.Param("program"), r, w.actor())
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, p.Find(r.ID))
}

// deleteRule removes a rule nothing has claimed; one that has claims is retired
// with active=false instead. SuperAdmin only.
func (h handlers) deleteRule(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	if _, err := h.e.DeleteRule(c.Context(), c.Param("program"), c.Param("rule"), w.actor()); err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, map[string]bool{"deleted": true})
}

// claims lists claims, newest first: the staff queue. ?status=pending is the queue
// itself; ?program= and ?limit= and ?offset= narrow it. SuperAdmin only.
func (h handlers) claims(c *zip.Ctx) error {
	if _, ok := platform(c); !ok {
		return nil
	}
	f, err := filter(c)
	if err != nil {
		return h.answer(c, err)
	}
	f.Subject = strings.TrimSpace(c.Query("subject"))
	rows, err := h.e.Claims(c.Context(), f)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"claims": rows})
}

// approve awards a claim: the points and badges to the ledger and the credit
// through commerce, each exactly once, and the staff member on the trail. Approving
// a claim already approved finishes it. SuperAdmin only.
func (h handlers) approve(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	got, err := h.e.Approve(c.Context(), c.Param("claim"), w.actor())
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, got)
}

type note struct {
	Note string `json:"note"`
}

// reject refuses a pending claim. The body is {"note": "why"}, which is required
// and kept with the claim and the trail. SuperAdmin only.
func (h handlers) reject(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	var n note
	if err := c.Bind(&n); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	got, err := h.e.Reject(c.Context(), c.Param("claim"), w.actor(), n.Note)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, got)
}

// reverse undoes an awarded claim with compensating ledger entries; what it wrote
// stays. The body is {"note": "why"}, which is required. A claim that granted credit
// cannot be reversed here. SuperAdmin only.
func (h handlers) reverse(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	var n note
	if err := c.Bind(&n); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	got, err := h.e.Reverse(c.Context(), c.Param("claim"), w.actor(), n.Note)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, got)
}

type trial struct {
	Program string        `json:"program"`
	Rule    rule.Rule     `json:"rule"`
	Event   catalog.Event `json:"event"`
}

// simulate is a dry run: what an event would do under a rule, limits and budget
// included, whether or not the rule is active or saved. Nothing is written.
// SuperAdmin only.
func (h handlers) simulate(c *zip.Ctx) error {
	if _, ok := platform(c); !ok {
		return nil
	}
	var t trial
	if err := c.Bind(&t); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	out, err := h.e.Simulate(c.Context(), t.Program, t.Rule, t.Event)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, out)
}

type past struct {
	Rows []ledger.Past `json:"rows"`
}

// backfill grandfathers in the points subjects earned before the ledger: the body is
// {"rows": [{"subject", "source", "points", "evidence"}]}, at most 5000. One row is
// one (subject, source) and is written once, so sending the same rows again changes
// nothing. The whole call is one record on the trail, naming who ran it. The points
// count toward lifetime points and levels and never toward credit. SuperAdmin only.
func (h handlers) backfill(c *zip.Ctx) error {
	w, ok := platform(c)
	if !ok {
		return nil
	}
	var in past
	if err := c.Bind(&in); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	out, err := h.e.Backfill(c.Context(), c.Param("program"), in.Rows, w.actor())
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, out)
}
