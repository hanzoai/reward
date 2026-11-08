package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/http"
	"github.com/hanzoai/reward/rule"
)

// who is a caller as the edge states it: the identity headers, and nothing else.
type who map[string]string

var (
	nobody   = who{}
	alice    = who{authz.HeaderUser: "alice", authz.HeaderUserEmail: "alice@example.com", authz.HeaderOrg: "alice-org"}
	bob      = who{authz.HeaderUser: "bob"}
	orgAdmin = who{authz.HeaderUser: "olga", authz.HeaderOrg: "acme", authz.HeaderUserOwner: "acme", authz.HeaderUserOrgAdmin: "true"}
	// an admin-org application: owner admin, but not the SuperAdmin verdict.
	adminApp = who{authz.HeaderUser: "app_x", authz.HeaderUserOwner: "admin", authz.HeaderOrg: "admin"}
	staff    = who{authz.HeaderUser: "z", authz.HeaderUserEmail: "z@hanzo.ai", authz.HeaderUserOwner: "admin", authz.HeaderUserAdmin: "true"}
)

type rig struct {
	t   *testing.T
	app *zip.App
	e   *engine.Engine
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{
		Path:   filepath.Join(t.TempDir(), "reward.db"),
		Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &engine.Engine{DB: db}
	app := zip.New(zip.Config{DisableStartupMessage: true})
	http.Route(app, e)
	return &rig{t: t, app: app, e: e}
}

func (r *rig) do(w who, method, path string, body any) (int, []byte, nethttp.Header) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w {
		req.Header.Set(k, v)
	}
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, resp.Header
}

func (r *rig) json(w who, method, path string, body any, want int, into any) {
	r.t.Helper()
	code, out, _ := r.do(w, method, path, body)
	if code != want {
		r.t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, out)
	}
	if into != nil {
		if err := json.Unmarshal(out, into); err != nil {
			r.t.Fatalf("%s %s: %v: %s", method, path, err, out)
		}
	}
}

// seeded stands up the seed program, and a small program of its own.
func (r *rig) seeded() {
	r.t.Helper()
	if _, err := r.e.Seed(t0()); err != nil {
		r.t.Fatal(err)
	}
}

const admin = "/v1/admin/reward"
const pub = "/v1/reward"

// every route that must refuse anyone but the platform principal.
var platformRoutes = []struct{ method, path string }{
	{"POST", pub + "/events"},
	{"GET", admin + "/programs"},
	{"POST", admin + "/programs"},
	{"GET", admin + "/programs/p"},
	{"PUT", admin + "/programs/p"},
	{"DELETE", admin + "/programs/p"},
	{"PUT", admin + "/programs/p/rules/r"},
	{"DELETE", admin + "/programs/p/rules/r"},
	{"POST", admin + "/programs/p/backfill"},
	{"GET", admin + "/claims"},
	{"POST", admin + "/claims/c/approve"},
	{"POST", admin + "/claims/c/reject"},
	{"POST", admin + "/claims/c/reverse"},
	{"POST", admin + "/simulate"},
}

func TestTheAdminGateRefusesEveryoneButTheSuperAdmin(t *testing.T) {
	r := newRig(t)
	for _, rt := range platformRoutes {
		for name, w := range map[string]who{"anonymous": nobody, "user": alice, "org admin": orgAdmin, "admin-org application": adminApp} {
			code, body, _ := r.do(w, rt.method, rt.path, map[string]any{})
			want := nethttp.StatusForbidden
			if name == "anonymous" {
				want = nethttp.StatusUnauthorized
			}
			if code != want {
				t.Errorf("%s %s as %s = %d, want %d: %s", rt.method, rt.path, name, code, want, body)
			}
		}
		code, body, _ := r.do(staff, rt.method, rt.path, map[string]any{})
		if code == nethttp.StatusUnauthorized || code == nethttp.StatusForbidden {
			t.Errorf("%s %s as the SuperAdmin = %d: %s", rt.method, rt.path, code, body)
		}
	}
}

func TestAForgedHeaderIsNotAnIdentity(t *testing.T) {
	r := newRig(t)
	// the admin flag with no subject is not a principal.
	code, _, _ := r.do(who{authz.HeaderUserAdmin: "true"}, "GET", admin+"/programs", nil)
	if code != nethttp.StatusUnauthorized {
		t.Errorf("admin flag alone = %d", code)
	}
	// only exactly "true" is the verdict.
	for _, v := range []string{"True", "1", "yes", "false", "truee"} {
		code, _, _ := r.do(who{authz.HeaderUser: "z", authz.HeaderUserAdmin: v}, "GET", admin+"/programs", nil)
		if code != nethttp.StatusForbidden {
			t.Errorf("X-User-IsAdmin=%q = %d, want 403", v, code)
		}
	}
}

func TestTheCatalogIsPublic(t *testing.T) {
	r := newRig(t)
	var doc struct {
		Pillars []struct {
			ID     string `json:"id"`
			Events []struct {
				Name string `json:"name"`
			} `json:"events"`
		} `json:"pillars"`
		Ops       []map[string]any `json:"ops"`
		Actions   []map[string]any `json:"actions"`
		Approvals []string         `json:"approvals"`
		Onces     []string         `json:"onces"`
	}
	r.json(nobody, "GET", pub+"/catalog", nil, 200, &doc)
	var ids []string
	events := 0
	for _, p := range doc.Pillars {
		ids = append(ids, p.ID)
		events += len(p.Events)
	}
	if strings.Join(ids, ",") != "use,share,community,contribute,expert" || events < 20 {
		t.Errorf("pillars %v, events %d", ids, events)
	}
	if len(doc.Ops) != 6 || len(doc.Actions) != 4 || len(doc.Approvals) != 2 || len(doc.Onces) != 3 {
		t.Errorf("vocabulary = %+v", doc)
	}
	for _, a := range doc.Actions {
		if a["id"] == "revshare" || a["id"] == "payout" {
			t.Errorf("action %v exists", a["id"])
		}
	}
	_, _, h := r.do(nobody, "GET", pub+"/catalog", nil)
	if !strings.Contains(h.Get("Cache-Control"), "public") {
		t.Errorf("Cache-Control = %q", h.Get("Cache-Control"))
	}
	r.json(nobody, "GET", pub+"/health", nil, 200, nil)
}

func TestIngestRefusesCallersWithoutThePlatformPrincipalAndFeedsClaimsToThePlatform(t *testing.T) {
	r := newRig(t)
	r.seeded()
	ev := map[string]any{"id": "gh-1", "type": "github.pr.merged", "subject": "alice",
		"fields": map[string]any{"repo": "hanzoai/commerce", "labels": []string{"bug"}, "kind": "fix"}}
	for name, w := range map[string]who{"anonymous": nobody, "user": alice, "org admin": orgAdmin} {
		if code, _, _ := r.do(w, "POST", pub+"/events", ev); code != 401 && code != 403 {
			t.Errorf("%s ingested: %d", name, code)
		}
	}
	var out engine.Outcome
	r.json(staff, "POST", pub+"/events", ev, 200, &out)
	var made int
	for _, d := range out.Decisions {
		if d.Result == "claimed" {
			made++
			if d.Claim.Status != "pending" || d.Claim.Subject != "alice" {
				t.Errorf("claim = %+v", d.Claim)
			}
		}
	}
	if made != 1 {
		t.Errorf("outcome = %+v", out)
	}
	// the same event again is a duplicate, not a second claim.
	r.json(staff, "POST", pub+"/events", ev, 200, &out)
	for _, d := range out.Decisions {
		if d.Rule == "pr-fix" && d.Result != "duplicate" {
			t.Errorf("second delivery = %+v", d)
		}
		if d.Result == "claimed" {
			t.Errorf("a redelivery made a claim: %+v", d)
		}
	}
	// an event outside the catalog is a 400.
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "x", "type": "github.nope", "subject": "alice", "fields": map[string]any{}}, 400, nil)
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "x", "type": "github.star", "subject": "alice", "fields": map[string]any{"repo": "a", "extra": 1}}, 400, nil)
}

func TestTheStaffQueueFromIngestToAwardAndBack(t *testing.T) {
	r := newRig(t)
	r.seeded()
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "gh-1", "type": "github.pr.merged", "subject": "alice",
		"fields": map[string]any{"repo": "hanzoai/commerce", "labels": []string{}, "kind": "fix"}}, 200, nil)

	var q struct {
		Claims []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Rule   string `json:"rule"`
		} `json:"claims"`
	}
	r.json(staff, "GET", admin+"/claims?status=pending", nil, 200, &q)
	if len(q.Claims) != 1 || q.Claims[0].Rule != "pr-fix" {
		t.Fatalf("queue = %+v", q)
	}
	id := q.Claims[0].ID
	r.json(staff, "GET", admin+"/claims?status=bogus", nil, 400, nil)

	// nothing is awarded until a person approves it.
	var me struct {
		Programs []struct {
			Lifetime   int64 `json:"lifetime"`
			Redeemable int64 `json:"redeemable"`
			Level      struct {
				Name string `json:"name"`
			} `json:"level"`
			Badges []struct {
				ID string `json:"id"`
			} `json:"badges"`
		} `json:"programs"`
	}
	r.json(alice, "GET", pub+"/me", nil, 200, &me)
	if me.Programs[0].Lifetime != 0 {
		t.Fatalf("awarded before approval: %+v", me)
	}

	// an org admin, or the user, cannot approve their own claim.
	for _, w := range []who{alice, orgAdmin} {
		if code, _, _ := r.do(w, "POST", admin+"/claims/"+id+"/approve", nil); code != 403 {
			t.Errorf("approve as a non-SuperAdmin = %d", code)
		}
	}
	r.json(staff, "POST", admin+"/claims/"+id+"/approve", nil, 200, nil)
	r.json(staff, "POST", admin+"/claims/"+id+"/approve", nil, 200, nil)
	r.json(alice, "GET", pub+"/me", nil, 200, &me)
	if p := me.Programs[0]; p.Lifetime != 75 || p.Redeemable != 75 || len(p.Badges) != 2 {
		t.Errorf("after approval: %+v", p)
	}

	r.json(staff, "POST", admin+"/claims/"+id+"/reject", map[string]string{"note": "no"}, 409, nil)
	r.json(staff, "POST", admin+"/claims/"+id+"/reverse", map[string]string{}, 400, nil)
	r.json(staff, "POST", admin+"/claims/"+id+"/reverse", map[string]string{"note": "fraud"}, 200, nil)
	r.json(alice, "GET", pub+"/me", nil, 200, &me)
	if p := me.Programs[0]; p.Lifetime != 0 || len(p.Badges) != 0 {
		t.Errorf("after reversal: %+v", p)
	}
	r.json(staff, "POST", admin+"/claims/clm_nope/approve", nil, 404, nil)
}

func TestMeIsTheCallersOwnRecordsOnly(t *testing.T) {
	r := newRig(t)
	r.seeded()
	for _, who := range []string{"alice", "bob"} {
		r.json(staff, "POST", pub+"/events", map[string]any{"id": "star-" + who, "type": "github.star", "subject": who, "fields": map[string]any{"repo": "hanzoai/" + who}}, 200, nil)
	}
	var mine struct {
		Claims []struct {
			Subject string `json:"subject"`
		} `json:"claims"`
	}
	r.json(alice, "GET", pub+"/me/claims", nil, 200, &mine)
	if len(mine.Claims) != 1 || mine.Claims[0].Subject != "alice" {
		t.Errorf("alice's claims = %+v", mine)
	}
	// a subject in the query is not a way to read someone else's.
	r.json(alice, "GET", pub+"/me/claims?subject=bob", nil, 200, &mine)
	if len(mine.Claims) != 1 || mine.Claims[0].Subject != "alice" {
		t.Errorf("?subject=bob read %+v", mine)
	}
	var me struct {
		Subject string `json:"subject"`
	}
	r.json(bob, "GET", pub+"/me", nil, 200, &me)
	if me.Subject != "bob" {
		t.Errorf("me = %+v", me)
	}
	for _, path := range []string{"/me", "/me/claims", "/me/explore", "/me/profile"} {
		if code, _, _ := r.do(nobody, "GET", pub+path, nil); code != 401 {
			t.Errorf("GET %s with no identity = %d", path, code)
		}
	}
}

func TestProgramsAndRulesAreEditedOverHTTP(t *testing.T) {
	r := newRig(t)
	prog := map[string]any{"id": "mini", "name": "Mini", "active": true,
		"badges": []map[string]any{{"id": "b1", "name": "One", "pillar": "use"}},
		"levels": []map[string]any{{"level": 1, "name": "Hacker", "minLifetimePoints": 0}}}
	r.json(staff, "POST", admin+"/programs", prog, 201, nil)
	r.json(staff, "POST", admin+"/programs", prog, 409, nil)
	r.json(staff, "POST", admin+"/programs", map[string]any{"id": "Bad Slug", "name": "x"}, 400, nil)

	star := map[string]any{"name": "Star", "active": true, "when": map[string]string{"event": "github.star"},
		"then": []map[string]any{{"type": "points", "points": 3}, {"type": "badge", "badge": "b1"}}}
	var saved rule.Rule
	r.json(staff, "PUT", admin+"/programs/mini/rules/star", star, 200, &saved)
	if saved.ID != "star" || saved.Version != 1 || saved.Approval != rule.Manual || saved.Pillar != "community" {
		t.Errorf("saved = %+v", saved)
	}
	r.json(staff, "PUT", admin+"/programs/mini/rules/star", star, 200, &saved)
	if saved.Version != 1 {
		t.Errorf("saving the same rule raised the version: %+v", saved)
	}
	star["then"] = []map[string]any{{"type": "points", "points": 4}}
	r.json(staff, "PUT", admin+"/programs/mini/rules/star", star, 200, &saved)
	if saved.Version != 2 {
		t.Errorf("version = %d", saved.Version)
	}
	r.json(staff, "PUT", admin+"/programs/mini/rules/other", map[string]any{"id": "star", "name": "x"}, 400, nil)
	r.json(staff, "PUT", admin+"/programs/mini/rules/bad", map[string]any{"name": "Bad", "when": map[string]string{"event": "revshare.paid"}, "then": []map[string]any{{"type": "revshare"}}}, 400, nil)
	r.json(staff, "PUT", admin+"/programs/nope/rules/star", star, 404, nil)

	// header PUT changes the header and keeps the rules.
	var p struct {
		Name    string      `json:"name"`
		Version int         `json:"version"`
		Rules   []rule.Rule `json:"rules"`
	}
	r.json(staff, "PUT", admin+"/programs/mini", map[string]any{"name": "Mini 2", "active": true,
		"badges": prog["badges"], "levels": prog["levels"], "rules": []any{}}, 200, &p)
	if p.Name != "Mini 2" || len(p.Rules) != 1 {
		t.Errorf("after header PUT: %+v", p)
	}
	r.json(staff, "PUT", admin+"/programs/ghost", prog, 404, nil)

	// simulate: a dry run that writes nothing.
	var sim engine.Outcome
	star["id"] = "star"
	r.json(staff, "POST", admin+"/simulate", map[string]any{"program": "mini", "rule": star,
		"event": map[string]any{"id": "s1", "type": "github.star", "subject": "alice", "fields": map[string]any{"repo": "a"}}}, 200, &sim)
	if !sim.Dry || sim.Decisions[0].Result != "claimed" || sim.Decisions[0].Claim.Points != 4 {
		t.Errorf("simulation = %+v", sim)
	}
	var q struct {
		Claims []any `json:"claims"`
	}
	r.json(staff, "GET", admin+"/claims", nil, 200, &q)
	if len(q.Claims) != 0 {
		t.Errorf("a simulation wrote claims: %+v", q)
	}

	// a claimed rule cannot be deleted; an unclaimed one can.
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "s1", "type": "github.star", "subject": "alice", "fields": map[string]any{"repo": "a"}}, 200, nil)
	r.json(staff, "DELETE", admin+"/programs/mini/rules/star", nil, 400, nil)
	r.json(staff, "DELETE", admin+"/programs/mini", nil, 400, nil)
	r.json(staff, "POST", admin+"/programs", map[string]any{"id": "empty", "name": "Empty"}, 201, nil)
	r.json(staff, "DELETE", admin+"/programs/empty", nil, 200, nil)
	r.json(staff, "GET", admin+"/programs/empty", nil, 404, nil)
	var all struct {
		Programs []struct {
			ID string `json:"id"`
		} `json:"programs"`
	}
	r.json(staff, "GET", admin+"/programs", nil, 200, &all)
	if len(all.Programs) != 1 || all.Programs[0].ID != "mini" {
		t.Errorf("programs = %+v", all)
	}
}

func TestBackfillOverHTTPIsAdminOnlyAndIdempotent(t *testing.T) {
	r := newRig(t)
	r.seeded()
	rows := map[string]any{"rows": []map[string]any{
		{"subject": "alice", "source": "usage.tokens", "points": 120, "evidence": "12M tokens"},
		{"subject": "alice", "source": "github.fork", "points": 2, "evidence": "2 forks"},
	}}
	if code, _, _ := r.do(orgAdmin, "POST", admin+"/programs/hanzo-rewards/backfill", rows); code != 403 {
		t.Errorf("org admin backfilled: %d", code)
	}
	var got engine.Backfilled
	r.json(staff, "POST", admin+"/programs/hanzo-rewards/backfill", rows, 200, &got)
	if got.Written != 2 || got.Points != 122 {
		t.Errorf("first = %+v", got)
	}
	r.json(staff, "POST", admin+"/programs/hanzo-rewards/backfill", rows, 200, &got)
	if got.Written != 0 || got.Skipped != 2 {
		t.Errorf("second = %+v", got)
	}
	var me struct {
		Programs []struct {
			Lifetime   int64 `json:"lifetime"`
			Redeemable int64 `json:"redeemable"`
			Level      struct {
				Name string `json:"name"`
			} `json:"level"`
		} `json:"programs"`
	}
	r.json(alice, "GET", pub+"/me", nil, 200, &me)
	if p := me.Programs[0]; p.Lifetime != 122 || p.Redeemable != 0 || p.Level.Name != "Builder" {
		t.Errorf("me = %+v", p)
	}
	r.json(staff, "POST", admin+"/programs/hanzo-rewards/backfill", map[string]any{"rows": []map[string]any{{"subject": "a", "source": "vibes", "points": 1}}}, 400, nil)
	r.json(staff, "POST", admin+"/programs/nope/backfill", rows, 404, nil)
}

func t0() context.Context { return context.Background() }
