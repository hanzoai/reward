package reward_test

import (
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward"
)

func open(t *testing.T) orm.DB {
	t.Helper()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{Path: filepath.Join(t.TempDir(), "r.db"), Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func get(t *testing.T, app *zip.App, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMountSeedsOnceAndServesTheCapability(t *testing.T) {
	db := open(t)
	app := zip.New(zip.Config{DisableStartupMessage: true})
	e, err := reward.Mount(app, reward.Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Program(t.Context(), "hanzo-rewards")
	if err != nil || len(p.Rules) < 90 {
		t.Fatalf("seed: %v %v", err, p)
	}
	if code, body := get(t, app, "/v1/reward/health", nil); code != 200 || !strings.Contains(body, `"reward"`) {
		t.Errorf("health = %d %s", code, body)
	}
	if code, body := get(t, app, "/v1/reward/catalog", nil); code != 200 || !strings.Contains(body, "github.badge") {
		t.Errorf("catalog = %d", code)
	}
	if code, _ := get(t, app, "/v1/admin/reward/programs", nil); code != 401 {
		t.Errorf("admin without identity = %d", code)
	}
	code, body := get(t, app, "/v1/admin/reward/programs", map[string]string{"X-User-Id": "z", "X-User-IsAdmin": "true"})
	if code != 200 || !strings.Contains(body, "hanzo-rewards") {
		t.Errorf("admin = %d", code)
	}

	// a second mount over the same store (another replica, a restart) keeps what admin did.
	r := *p.Find("tokens-100k")
	r.Then[0].Points = 11
	if _, err := e.SaveRule(t.Context(), "hanzo-rewards", r, "z"); err != nil {
		t.Fatal(err)
	}
	e2, err := reward.Mount(zip.New(zip.Config{DisableStartupMessage: true}), reward.Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e2.Program(t.Context(), "hanzo-rewards")
	if got := p.Find("tokens-100k").Then[0].Points; got != 11 {
		t.Errorf("a remount reseeded: points = %d", got)
	}
}

func TestMountRefusesWhatItCannotRun(t *testing.T) {
	if _, err := reward.Mount(nil, reward.Config{DB: open(t)}); err == nil {
		t.Error("nil app")
	}
	if _, err := reward.Mount(zip.New(zip.Config{DisableStartupMessage: true}), reward.Config{}); err == nil {
		t.Error("no store")
	}
}
