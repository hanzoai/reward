package http_test

import (
	"strings"
	"testing"

	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/rule"
)

// earn gives alice an approved star (+1, badge) and a backfilled history.
func (r *rig) earn() {
	r.t.Helper()
	r.seeded()
	var out engine.Outcome
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "star-1", "type": "github.star", "subject": "alice", "email": "alice@example.com",
		"fields": map[string]any{"repo": "hanzoai/commerce"}, "evidence": map[string]any{"source": "github", "url": "https://github.com/hanzoai/x", "needs_review": true}}, 200, &out)
	for _, d := range out.Decisions {
		if d.Claim != nil {
			r.json(staff, "POST", admin+"/claims/"+d.Claim.Id()+"/approve", nil, 200, nil)
		}
	}
	r.json(staff, "POST", admin+"/programs/hanzo-rewards/backfill", map[string]any{"rows": []map[string]any{
		{"subject": "alice", "source": "usage.tokens", "points": 120, "evidence": "12M tokens"}}}, 200, nil)
}

func TestProfileIsPrivateByDefaultAndPublicOnlyWhenTheOwnerSaysSo(t *testing.T) {
	r := newRig(t)
	r.earn()

	var mine struct {
		Handle string `json:"handle"`
		Public bool   `json:"public"`
	}
	r.json(alice, "GET", pub+"/me/profile", nil, 200, &mine)
	if mine.Public || mine.Handle != "" {
		t.Errorf("a new profile = %+v", mine)
	}
	r.json(nobody, "GET", pub+"/profile/alice", nil, 404, nil)

	// invalid input is a 400, and the owner is the one in the headers, never the body.
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"public": true}, 400, nil)
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "links": map[string]string{"github": "http://github.com/alice"}}, 400, nil)
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "links": map[string]string{"website": "javascript:alert(1)"}}, 400, nil)
	r.json(nobody, "PUT", pub+"/me/profile", map[string]any{"handle": "ghost"}, 401, nil)

	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "Alice", "public": true,
		"links": map[string]string{"github": "https://github.com/alice", "x": "https://x.com/alice", "website": "https://alice.dev"}}, 200, &mine)
	if !mine.Public || mine.Handle != "alice" {
		t.Errorf("saved = %+v", mine)
	}
	// someone else cannot take the handle, and cannot write alice's profile.
	r.json(bob, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 409, nil)

	code, body, h := r.do(nobody, "GET", pub+"/profile/alice", nil)
	if code != 200 {
		t.Fatalf("public profile = %d: %s", code, body)
	}
	if !strings.Contains(h.Get("Cache-Control"), "public") {
		t.Errorf("Cache-Control = %q", h.Get("Cache-Control"))
	}
	var view struct {
		Handle    string `json:"handle"`
		Level     int    `json:"level"`
		LevelName string `json:"levelName"`
		Points    int64  `json:"points"`
		Badges    []struct {
			ID, Name, Pillar, Rarity, EarnedAt string
		} `json:"badges"`
		Pillars map[string]int    `json:"pillars"`
		Links   map[string]string `json:"links"`
	}
	r.json(nobody, "GET", pub+"/profile/ALICE", nil, 200, &view)
	if view.Handle != "alice" || view.Points != 121 || view.Level != 2 || view.LevelName != "Builder" {
		t.Errorf("view = %+v", view)
	}
	if len(view.Badges) != 1 || view.Badges[0].ID != "stargazer" || view.Badges[0].Rarity != "common" || view.Badges[0].EarnedAt == "" {
		t.Errorf("badges = %+v", view.Badges)
	}
	if view.Pillars["community"] != 1 || len(view.Pillars) != 5 {
		t.Errorf("pillars = %+v", view.Pillars)
	}
	if view.Links["github"] != "https://github.com/alice" || view.Links["website"] != "https://alice.dev" {
		t.Errorf("links = %+v", view.Links)
	}
	// never an email, an org, a subject id or a redeemable balance.
	for _, secret := range []string{"alice@example.com", "alice-org", "redeemable", `"subject"`, `"email"`, "p:alice"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("the public profile carries %q: %s", secret, body)
		}
	}

	// turning it off hides it again.
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": false}, 200, nil)
	r.json(nobody, "GET", pub+"/profile/alice", nil, 404, nil)
	r.json(nobody, "GET", pub+"/profile/nobody-here", nil, 404, nil)
}

func TestExploreIsTheCallersOwnListByPillar(t *testing.T) {
	r := newRig(t)
	r.earn()
	var doc struct {
		Pillars []struct {
			ID    string `json:"id"`
			Rules []struct {
				Rule   string `json:"rule"`
				Points int64  `json:"points"`
				Badges []struct {
					ID string `json:"id"`
				} `json:"badges"`
				Event struct {
					Name string `json:"name"`
				} `json:"event"`
			} `json:"rules"`
		} `json:"pillars"`
	}
	r.json(alice, "GET", pub+"/me/explore", nil, 200, &doc)
	var ids []string
	for _, p := range doc.Pillars {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "use,share,community,contribute,expert" {
		t.Fatalf("pillars = %v", ids)
	}
	has := func(pillar int, rule string) bool {
		for _, x := range doc.Pillars[pillar].Rules {
			if x.Rule == rule {
				return true
			}
		}
		return false
	}
	// alice starred, so github-star is done for her; follow is not. Inactive seed rules are not offered.
	if has(2, "github-star") || !has(2, "github-follow") {
		t.Errorf("community = %+v", doc.Pillars[2].Rules)
	}
	if has(0, "try-dev") {
		t.Error("an inactive rule is offered")
	}
	if !has(3, "pr-fix") || !has(3, "github-badge") {
		t.Errorf("contribute = %+v", doc.Pillars[3].Rules)
	}
	for _, x := range doc.Pillars[3].Rules {
		if x.Rule == "github-badge" && (x.Points != 10 || x.Event.Name != "github.badge") {
			t.Errorf("github-badge = %+v", x)
		}
	}
	r.json(bob, "GET", pub+"/me/explore", nil, 200, &doc)
	if !has(2, "github-star") {
		t.Error("bob did not star anything and is not offered the star rule")
	}
}

func TestTheBadgeSVGIsPublicOnlyWithTheProfileAndRefetchedByCamo(t *testing.T) {
	r := newRig(t)
	r.earn()
	// private profile: no image, same 404 as no such handle.
	if code, _, _ := r.do(nobody, "GET", pub+"/badge/alice.svg", nil); code != 404 {
		t.Fatalf("private badge = %d", code)
	}
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 200, nil)

	code, svg, h := r.do(nobody, "GET", pub+"/badge/alice.svg", nil)
	if code != 200 {
		t.Fatalf("badge = %d: %s", code, svg)
	}
	if got := h.Get("Content-Type"); !strings.HasPrefix(got, "image/svg+xml") {
		t.Errorf("Content-Type = %q", got)
	}
	if cc := h.Get("Cache-Control"); cc != "no-cache, max-age=0" {
		t.Errorf("Cache-Control = %q", cc)
	}
	tag := h.Get("ETag")
	if tag == "" || !strings.HasPrefix(tag, `"`) {
		t.Errorf("ETag = %q", tag)
	}
	if !strings.Contains(string(svg), "Builder") || !strings.Contains(string(svg), "121 pts") {
		t.Errorf("svg = %s", svg)
	}

	// a revalidation with the same tag is a 304; a changed standing is a new tag.
	req := who{"If-None-Match": tag}
	if code, body, _ := r.do(req, "GET", pub+"/badge/alice.svg", nil); code != 304 || len(body) != 0 {
		t.Errorf("revalidation = %d (%d bytes)", code, len(body))
	}
	r.json(staff, "POST", admin+"/programs/hanzo-rewards/backfill", map[string]any{"rows": []map[string]any{
		{"subject": "alice", "source": "github.fork", "points": 9, "evidence": "9 forks"}}}, 200, nil)
	code, svg2, h2 := r.do(req, "GET", pub+"/badge/alice.svg", nil)
	if code != 200 || h2.Get("ETag") == tag || !strings.Contains(string(svg2), "130 pts") {
		t.Errorf("after the standing changed: %d %q %s", code, h2.Get("ETag"), svg2)
	}

	// options.
	for _, q := range []string{"?theme=light", "?style=card", "?theme=dark&style=card", "?theme=light&style=flat"} {
		if code, body, _ := r.do(nobody, "GET", pub+"/badge/alice.svg"+q, nil); code != 200 || !strings.HasPrefix(string(body), "<svg") {
			t.Errorf("%s = %d", q, code)
		}
	}
	for _, q := range []string{"?theme=neon", "?style=huge"} {
		if code, _, _ := r.do(nobody, "GET", pub+"/badge/alice.svg"+q, nil); code != 400 {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
	// without the .svg it is not the image route's address.
	if code, _, _ := r.do(nobody, "GET", pub+"/badge/alice", nil); code != 404 {
		t.Errorf("no suffix = %d", code)
	}
}

func TestTheBadgeSVGEscapesWhatAnAdminTyped(t *testing.T) {
	r := newRig(t)
	r.earn()
	prog, err := r.e.Program(t0(), "hanzo-rewards")
	if err != nil {
		t.Fatal(err)
	}
	levels := append([]rule.Level(nil), prog.Levels...)
	levels[1].Name = `Build<script>alert("x")</script>&`
	var in rule.Program
	in.Name, in.Active, in.Budget, in.Levels, in.Badges, in.Templates = prog.Name, prog.Active, prog.Budget, levels, prog.Badges, prog.Templates
	if _, err := r.e.SaveProgram(t0(), "hanzo-rewards", in, "z"); err != nil {
		t.Fatal(err)
	}
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 200, nil)
	for _, style := range []string{"flat", "card"} {
		_, svg, _ := r.do(nobody, "GET", pub+"/badge/alice.svg?style="+style, nil)
		if strings.Contains(string(svg), "<script") || strings.Contains(string(svg), `alert("x")`) {
			t.Errorf("%s: unescaped text in %s", style, svg)
		}
		if !strings.Contains(string(svg), "&lt;script&gt;") {
			t.Errorf("%s: the text is missing: %s", style, svg)
		}
	}
}
