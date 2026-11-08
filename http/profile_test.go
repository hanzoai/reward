package http_test

import (
	"slices"
	"strings"
	"testing"
	"time"

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
		Band      string `json:"band"`
		Points    int64  `json:"points"`
		Badges    []struct {
			ID, Name, Pillar, Rarity, EarnedAt string
		} `json:"badges"`
		Pillars map[string]int    `json:"pillars"`
		Links   map[string]string `json:"links"`
	}
	r.json(nobody, "GET", pub+"/profile/ALICE", nil, 200, &view)
	if view.Handle != "alice" || view.Points != 121 || view.Level != 3 || view.LevelName != "Photon 3" || view.Band != "Photon" {
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
	// the tenure, streak, founder and referral rules are on, so they are offered, each under its pillar.
	if !has(0, "tenure-6") || !has(0, "streak-30") || !has(1, "referral-network") || !has(1, "referral-milestone-550") || !has(2, "founding-developer") {
		t.Errorf("use = %+v, share = %+v, community = %+v", doc.Pillars[0].Rules, doc.Pillars[1].Rules, doc.Pillars[2].Rules)
	}
	for _, x := range doc.Pillars[1].Rules {
		if x.Rule == "referral-milestone-550" && (x.Points != 0 || len(x.Badges) != 1 || x.Badges[0].ID != "cosmos-x" || x.Event.Name != "referral.milestone") {
			t.Errorf("referral-milestone-550 = %+v", x)
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
	if !strings.Contains(string(svg), "Photon 3") || !strings.Contains(string(svg), "121 pts") {
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
	levels[3].Name = `Build<script>alert("x")</script>&`
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

// award posts an event about alice and approves every claim it makes.
func (r *rig) award(id, typ string, fields map[string]any) {
	r.t.Helper()
	var out engine.Outcome
	r.json(staff, "POST", pub+"/events", map[string]any{"id": id, "type": typ, "subject": "alice", "email": "alice@example.com", "fields": fields}, 200, &out)
	for _, d := range out.Decisions {
		if d.Claim != nil {
			r.json(staff, "POST", admin+"/claims/"+d.Claim.Id()+"/approve", nil, 200, nil)
		}
	}
}

// rarities sets the rarity of badges of the seed program by id.
func (r *rig) rarities(by map[string]rule.Rarity) {
	r.t.Helper()
	prog, err := r.e.Program(t0(), "hanzo-rewards")
	if err != nil {
		r.t.Fatal(err)
	}
	badges := slices.Clone(prog.Badges)
	for i := range badges {
		if x, ok := by[badges[i].ID]; ok {
			badges[i].Rarity = x
		}
	}
	in := rule.Program{Name: prog.Name, Active: prog.Active, Budget: prog.Budget, Upline: prog.Upline, Levels: prog.Levels, Badges: badges, Templates: prog.Templates}
	if _, err := r.e.SaveProgram(t0(), "hanzo-rewards", in, "z"); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) svg(path string) string {
	r.t.Helper()
	code, body, h := r.do(nobody, "GET", pub+"/badge/"+path, nil)
	if code != 200 {
		r.t.Fatalf("%s = %d: %s", path, code, body)
	}
	if cc := h.Get("Cache-Control"); cc != "no-cache, max-age=0" {
		r.t.Errorf("%s: Cache-Control = %q", path, cc)
	}
	return string(body)
}

func TestTheBadgeSVGSaysTheFieldsAskedInTheOrderGiven(t *testing.T) {
	r := newRig(t)
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	r.e.Now = func() time.Time { return clock }
	r.earn()
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 200, nil)

	// the default, and a label of one's own.
	svg := r.svg("alice.svg")
	if !strings.Contains(svg, ">Hanzo</text>") || !strings.Contains(svg, "Photon 3 · 121 pts") || strings.Contains(svg, "Stargazer") {
		t.Errorf("default = %s", svg)
	}
	if svg := r.svg("alice.svg?label=Zoo"); !strings.Contains(svg, ">Zoo</text>") || strings.Contains(svg, "Hanzo") {
		t.Errorf("label = %s", svg)
	}
	if svg := r.svg("alice.svg?label=%3Cb%3E%26%22x"); !strings.Contains(svg, "&lt;b&gt;&amp;&#34;x") || strings.Contains(svg, "<b>") {
		t.Errorf("label markup = %s", svg)
	}
	if svg := r.svg("alice.svg?fields=points,level"); strings.Index(svg, "121 pts") > strings.Index(svg, "Photon 3") || !strings.Contains(svg, "121 pts · Photon 3") {
		t.Errorf("order = %s", svg)
	}
	if svg := r.svg("alice.svg?fields=level"); strings.Contains(svg, "pts") {
		t.Errorf("level only = %s", svg)
	}

	// no token milestone yet: the field is left out, and nothing is drawn empty.
	svg = r.svg("alice.svg?fields=level,tokens")
	if !strings.Contains(svg, "Photon 3</text>") || strings.Contains(svg, "·") {
		t.Errorf("no tier = %s", svg)
	}
	svg = r.svg("alice.svg?fields=tokens")
	if strings.Count(svg, "<text") != 1 {
		t.Errorf("nothing to say = %s", svg)
	}

	// 5M tokens earns the 100K, 1M and 5M badges: the tier is the highest, by its name.
	clock = clock.Add(time.Hour)
	r.award("tok-5m", "usage.tokens", map[string]any{"tokens": 5_000_000, "model": "zen4"})
	svg = r.svg("alice.svg?fields=tokens")
	if !strings.Contains(svg, "Five Million") || strings.Contains(svg, "Million Token Club") || strings.Contains(svg, "First 100K") {
		t.Errorf("tier = %s", svg)
	}
	// 10B tokens reaches Titan past Ten Million, Century and Billionaire in one event;
	// making the lowest tier the rarest changes nothing about which is highest.
	r.rarities(map[string]rule.Rarity{"first-100k": rule.Legendary})
	clock = clock.Add(time.Hour)
	r.award("tok-10b", "usage.tokens", map[string]any{"tokens": 10_000_000_000, "model": "zen4"})
	svg = r.svg("alice.svg?fields=level,points,tokens")
	if !strings.Contains(svg, "Photon 7 · ") || !strings.Contains(svg, " pts · Titan") {
		t.Errorf("highest tier = %s", svg)
	}
	for _, count := range []string{"5000000", "10000000000", "5M", "10B"} {
		if strings.Contains(svg, count) {
			t.Errorf("a token count %s is on the picture: %s", count, svg)
		}
	}

	// alice holds eight badges. First 100K is legendary; Stargazer, earned first, and
	// Titan, earned last, are epic: the top three are the legendary one and then the
	// epic ones, the most recently earned first (not by name: Stargazer sorts before
	// Titan).
	r.rarities(map[string]rule.Rarity{"first-100k": rule.Legendary, "stargazer": rule.Epic, "titan": rule.Epic})
	svg = r.svg("alice.svg?fields=badges&style=card")
	names := []string{"First 100K", "Titan", "Stargazer"}
	at := -1
	for _, n := range names {
		i := strings.Index(svg, ">"+n+"<")
		if i < at || i < 0 {
			t.Errorf("%q is not drawn after the one before it: %s", n, svg)
		}
		at = i
	}
	if strings.Count(svg, "<circle") != 3 || strings.Contains(svg, "Million") || strings.Contains(svg, "Century") {
		t.Errorf("only the top three are drawn: %s", svg)
	}
	// the top three as one line, after the other fields.
	if svg := r.svg("alice.svg?fields=level,badges"); !strings.Contains(svg, "Photon 7 · First 100K, Titan, Stargazer") {
		t.Errorf("flat badges = %s", svg)
	}

	// everyone's picture is their own: bob holds nothing and his badges are left out.
	r.json(bob, "PUT", pub+"/me/profile", map[string]any{"handle": "bob", "public": true}, 200, nil)
	if svg := r.svg("bob.svg?fields=level,tokens,badges&style=card"); strings.Contains(svg, "<circle") || strings.Contains(svg, "no badges") || !strings.Contains(svg, "Photon 0") {
		t.Errorf("bob = %s", svg)
	}
	// and the card draws the four fields in the order given: the fields before the
	// badges are on the line under the handle.
	svg = r.svg("alice.svg?fields=badges,tokens,points,level&style=card")
	if !strings.Contains(svg, "Titan · ") || !strings.Contains(svg, " pts · Photon 7<") {
		t.Errorf("card = %s", svg)
	}
}

// every query the route refuses, with the reason it is wrong.
var badOptions = map[string]string{
	"unknown field":   "fields=level,rank",
	"duplicate field": "fields=level,points,level",
	"empty fields":    "fields=",
	"empty name":      "fields=level,,points",
	"trailing comma":  "fields=level,",
	"field case":      "fields=Level",
	"label 25":        "label=" + strings.Repeat("a", 25),
	"label 25 runes":  "label=" + strings.Repeat("%C3%A9", 25),
	"empty label":     "label=",
	"newline":         "label=a%0Ab",
	"carriage return": "label=a%0Db",
	"tab":             "label=a%09b",
	"nul":             "label=a%00b",
	"delete":          "label=a%7Fb",
	"invalid utf-8":   "label=a%FFb",
	"theme":           "theme=neon",
	"style":           "style=huge",
	"one bad of many": "theme=light&style=card&fields=level,points&label=" + strings.Repeat("a", 25),
}

func TestTheBadgeSVGRefusesWhatItCannotDraw(t *testing.T) {
	r := newRig(t)
	r.earn()
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 200, nil)
	for name, q := range badOptions {
		var doc struct {
			Error struct{ Type, Message string } `json:"error"`
		}
		r.json(nobody, "GET", pub+"/badge/alice.svg?"+q, nil, 400, &doc)
		if doc.Error.Type != "api-error" || doc.Error.Message == "" {
			t.Errorf("%s: the refusal is %+v", name, doc)
		}
	}
	// the longest label, in runes and not bytes, and every field, are drawn.
	for _, q := range []string{"label=" + strings.Repeat("a", 24), "label=" + strings.Repeat("%C3%A9", 24), "fields=badges,tokens,points,level", "theme=&style=&fields=level", "label=a+b%20c"} {
		r.svg("alice.svg?" + q)
	}
}

// Options are read before the handle, so a bad one is the same 400 for a public
// profile, a private one and a handle nobody holds: it cannot be used to ask whether
// a handle exists. A good request for the last two is the same 404.
func TestABadOptionNeverSaysWhetherTheHandleExists(t *testing.T) {
	r := newRig(t)
	r.earn()
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 200, nil)
	r.json(bob, "PUT", pub+"/me/profile", map[string]any{"handle": "bob", "public": false}, 200, nil)
	for name, q := range badOptions {
		var want []byte
		for _, handle := range []string{"alice", "bob", "ghost"} {
			code, body, _ := r.do(nobody, "GET", pub+"/badge/"+handle+".svg?"+q, nil)
			if code != 400 {
				t.Errorf("%s: %s = %d, want 400", name, handle, code)
			}
			if want == nil {
				want = body
			} else if string(body) != string(want) {
				t.Errorf("%s: %s answers %s, not %s", name, handle, body, want)
			}
		}
	}
	var want []byte
	for _, handle := range []string{"bob", "ghost"} {
		code, body, h := r.do(nobody, "GET", pub+"/badge/"+handle+".svg?fields=level,tokens&label=x", nil)
		if code != 404 || h.Get("ETag") != "" {
			t.Errorf("%s = %d", handle, code)
		}
		if want == nil {
			want = body
		} else if string(body) != string(want) {
			t.Errorf("%s answers %s, not %s", handle, body, want)
		}
	}
}

func TestTheBadgeSVGRevalidatesPerOptionAndItsTagFollowsThePicture(t *testing.T) {
	r := newRig(t)
	r.earn()
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 200, nil)
	r.award("tok-1m", "usage.tokens", map[string]any{"tokens": 1_000_000, "model": "zen4"})

	queries := []string{"", "theme=light", "style=card", "fields=level", "fields=points,level", "fields=level,points,tokens", "fields=level,badges&style=card", "label=Zoo", "label=Hanzo+rewards&fields=level,tokens"}
	tags := map[string]string{}
	for _, q := range queries {
		code, _, h := r.do(nobody, "GET", pub+"/badge/alice.svg?"+q, nil)
		tag := h.Get("ETag")
		if code != 200 || tag == "" || h.Get("Cache-Control") != "no-cache, max-age=0" {
			t.Fatalf("%q = %d %q %q", q, code, tag, h.Get("Cache-Control"))
		}
		if prev, dup := tags[tag]; dup {
			t.Errorf("%q and %q are different pictures with the same tag %s", prev, q, tag)
		}
		tags[tag] = q
		// the tag revalidates its own picture: 304 and no body, with the same headers.
		code, body, h2 := r.do(who{"If-None-Match": tag}, "GET", pub+"/badge/alice.svg?"+q, nil)
		if code != 304 || len(body) != 0 || h2.Get("ETag") != tag || h2.Get("Cache-Control") != "no-cache, max-age=0" {
			t.Errorf("%q revalidation = %d (%d bytes) %q %q", q, code, len(body), h2.Get("ETag"), h2.Get("Cache-Control"))
		}
		// and no other option's picture: the tag of this one asked of the default is a 200.
		if q != "" {
			if code, _, _ := r.do(who{"If-None-Match": tag}, "GET", pub+"/badge/alice.svg", nil); code != 200 {
				t.Errorf("%q: its tag revalidated the default picture: %d", q, code)
			}
		}
	}

	// the same picture said another way is the same tag: ?theme= and ?style= empty are the defaults.
	_, _, a := r.do(nobody, "GET", pub+"/badge/alice.svg", nil)
	_, _, b := r.do(nobody, "GET", pub+"/badge/alice.svg?theme=dark&style=flat&fields=level,points&label=Hanzo", nil)
	if a.Get("ETag") != b.Get("ETag") {
		t.Errorf("the default spelled out has another tag: %s %s", a.Get("ETag"), b.Get("ETag"))
	}

	// the tag moves when the standing does, whatever the options.
	_, _, before := r.do(nobody, "GET", pub+"/badge/alice.svg?fields=tokens&label=Zoo", nil)
	r.award("tok-5m", "usage.tokens", map[string]any{"tokens": 5_000_000, "model": "zen4"})
	code, _, after := r.do(who{"If-None-Match": before.Get("ETag")}, "GET", pub+"/badge/alice.svg?fields=tokens&label=Zoo", nil)
	if code != 200 || after.Get("ETag") == before.Get("ETag") {
		t.Errorf("after a new tier: %d %s %s", code, before.Get("ETag"), after.Get("ETag"))
	}
}

func TestMeAndThePublicProfileCarryTheBandOfTheLevel(t *testing.T) {
	r := newRig(t)
	r.seeded()
	r.json(alice, "PUT", pub+"/me/profile", map[string]any{"handle": "alice", "public": true}, 200, nil)
	type level struct {
		Level int    `json:"level"`
		Name  string `json:"name"`
		Band  string `json:"band"`
	}
	var me struct {
		Programs []struct {
			Lifetime int64  `json:"lifetime"`
			Level    level  `json:"level"`
			Next     *level `json:"next"`
			Band     string `json:"band"`
		} `json:"programs"`
	}
	var view struct {
		Level     int    `json:"level"`
		LevelName string `json:"levelName"`
		Band      string `json:"band"`
		Points    int64  `json:"points"`
	}
	check := func(points int64, n int, name, band, next string) {
		t.Helper()
		r.json(alice, "GET", pub+"/me", nil, 200, &me)
		p := me.Programs[0]
		if p.Lifetime != points || p.Level != (level{n, name, band}) || p.Band != band || p.Next == nil || p.Next.Name != next {
			t.Errorf("/me at %d points = %+v (next %+v)", points, p, p.Next)
		}
		r.json(nobody, "GET", pub+"/profile/alice", nil, 200, &view)
		if view.Points != points || view.Level != n || view.LevelName != name || view.Band != band {
			t.Errorf("the profile at %d points = %+v", points, view)
		}
		// the badge draws the level's name and nothing of the band beside it.
		if svg := r.svg("alice.svg"); !strings.Contains(svg, name+" · ") {
			t.Errorf("the badge at %d points = %s", points, svg)
		}
	}
	check(0, 0, "Photon 0", "Photon", "Photon 1")
	back := func(source string, points int) {
		t.Helper()
		r.json(staff, "POST", admin+"/programs/hanzo-rewards/backfill", map[string]any{"rows": []map[string]any{{"subject": "alice", "source": source, "points": points, "evidence": "x"}}}, 200, nil)
	}
	back("usage.tokens", 999)
	check(999, 9, "Photon 9", "Photon", "Wave 10")
	back("github.star", 1)
	check(1000, 10, "Wave 10", "Wave", "Wave 11")
	back("github.fork", 24_000)
	check(25_000, 50, "Pulsar 50", "Pulsar", "Pulsar 51")
}

func TestAProgramWithItsOwnLevelsHasNoBandUntilItNamesOne(t *testing.T) {
	r := newRig(t)
	r.json(staff, "POST", admin+"/programs", map[string]any{"id": "mini", "name": "Mini", "active": true,
		"levels": []map[string]any{{"level": 1, "name": "Hacker", "minLifetimePoints": 0}, {"level": 2, "name": "Builder 2", "band": "Builder", "minLifetimePoints": 100}}}, 201, nil)
	var me struct {
		Programs []struct {
			Level struct {
				Level int    `json:"level"`
				Name  string `json:"name"`
				Band  string `json:"band"`
			} `json:"level"`
			Band string `json:"band"`
		} `json:"programs"`
	}
	r.json(alice, "GET", pub+"/me", nil, 200, &me)
	if p := me.Programs[0]; p.Level.Level != 1 || p.Level.Name != "Hacker" || p.Band != "Hacker" || p.Level.Band != "Hacker" {
		t.Errorf("me = %+v", p)
	}
	// the admin table is as it was typed: an empty band stays empty there.
	var prog rule.View
	r.json(staff, "GET", admin+"/programs/mini", nil, 200, &prog)
	if prog.Levels[0].Band != "" || prog.Levels[1].Band != "Builder" {
		t.Errorf("levels = %+v", prog.Levels)
	}
}
