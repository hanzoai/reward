package credit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/rule"
)

// commerce stands in for commerce's POST /v1/billing/credit: it takes the request
// the route takes and credits once per idempotencyKey, answering 201 and then 200.
type commerce struct {
	mu     sync.Mutex
	grants map[string]request
	paths  []string
	fail   int
}

func (c *commerce) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)
	if c.fail != 0 {
		http.Error(w, "no", c.fail)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var req request
	if err := json.Unmarshal(raw, &req); err != nil || req.Org == "" || req.AmountCents <= 0 || req.Reason == "" || req.IdempotencyKey == "" {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	if _, ok := c.grants[req.IdempotencyKey]; ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	c.grants[req.IdempotencyKey] = req
	w.WriteHeader(http.StatusCreated)
}

func TestGrantPostsToCommercesOneCreditPath(t *testing.T) {
	c := &commerce{grants: map[string]request{}}
	srv := httptest.NewServer(c)
	defer srv.Close()
	cl := Client{Base: srv.URL + "/"}
	g := engine.Grant{Org: "acme", Cents: 500, Reason: "program p, rule r", Tag: "reward:p/r", Actor: "z@hanzo.ai", Key: "reward:clm_1:0"}
	for range 2 {
		if err := cl.Grant(context.Background(), g); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.grants) != 1 || len(c.paths) != 2 || c.paths[0] != "POST /v1/billing/credit" {
		t.Fatalf("grants %d, calls %v", len(c.grants), c.paths)
	}
	got := c.grants["reward:clm_1:0"]
	if got.Org != "acme" || got.AmountCents != 500 || got.Tag != "reward:p/r" || got.Reason == "" {
		t.Errorf("request = %+v", got)
	}
}

func TestAnErrorFromCommerceIsAnError(t *testing.T) {
	c := &commerce{grants: map[string]request{}, fail: http.StatusForbidden}
	srv := httptest.NewServer(c)
	defer srv.Close()
	err := Client{Base: srv.URL}.Grant(context.Background(), engine.Grant{Org: "acme", Cents: 1, Reason: "r", Key: "k"})
	if err == nil {
		t.Fatal("a 403 from commerce was taken as a grant")
	}
	if err := (Client{Base: "http://127.0.0.1:1"}).Grant(context.Background(), engine.Grant{Org: "acme", Cents: 1, Reason: "r", Key: "k"}); err == nil {
		t.Fatal("an unreachable commerce was taken as a grant")
	}
}

func TestCeilingDefaultsToCommercesAndCanBeLowered(t *testing.T) {
	if (Client{}).Ceiling() != DefaultMax || (Client{Max: 5000}).Ceiling() != 5000 {
		t.Error("ceiling")
	}
}

type recording struct{ got []*http.Request }

func (r *recording) Do(req *http.Request) (*http.Response, error) {
	r.got = append(r.got, req)
	return &http.Response{StatusCode: 201, Body: io.NopCloser(http.NoBody)}, nil
}

func TestTheHostCarriesTheRequest(t *testing.T) {
	d := &recording{}
	if err := (Client{Base: "http://commerce", Doer: d}).Grant(context.Background(), engine.Grant{Org: "o", Cents: 1, Reason: "r", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if len(d.got) != 1 || d.got[0].URL.String() != "http://commerce/v1/billing/credit" {
		t.Errorf("requests = %+v", d.got)
	}
}

func TestAnApprovedCreditRuleGrantsOnceThroughCommerce(t *testing.T) {
	c := &commerce{grants: map[string]request{}}
	srv := httptest.NewServer(c)
	defer srv.Close()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{Path: filepath.Join(t.TempDir(), "r.db"), Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := &engine.Engine{DB: db, Credits: Client{Base: srv.URL}}
	ctx := context.Background()
	prog := rule.Program{Name: "P", Active: true, Rules: []rule.Rule{{
		ID: "c", Name: "c", Active: true, When: rule.When{Event: "github.star"},
		Then: []rule.Action{{Type: rule.Do.Credit, Cents: 250}},
	}}}
	if _, err := e.SaveProgram(ctx, "p", prog, "z"); err != nil {
		t.Fatal(err)
	}
	out, err := e.Evaluate(ctx, catalog.Event{ID: "e1", Type: "github.star", Subject: "alice", Org: "acme", Fields: map[string]any{"repo": "r"}})
	if err != nil || len(out.Decisions) != 1 {
		t.Fatalf("evaluate: %v %+v", err, out)
	}
	if len(c.grants) != 0 {
		t.Fatal("credit was granted before a person approved the claim")
	}
	id := out.Decisions[0].Claim.Id()
	for range 3 {
		if _, err := e.Approve(ctx, id, "staff"); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.grants) != 1 {
		t.Errorf("grants = %d, want 1", len(c.grants))
	}
	// and a rule above the ceiling is refused up front.
	big := rule.Rule{ID: "big", Name: "big", When: rule.When{Event: "github.star"}, Then: []rule.Action{{Type: rule.Do.Credit, Cents: DefaultMax + 1}}}
	if _, err := e.SaveRule(ctx, "p", big, "z"); err == nil {
		t.Error("a credit above commerce's ceiling was saved")
	}
}
