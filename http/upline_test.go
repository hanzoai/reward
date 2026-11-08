package http_test

import (
	"strings"
	"testing"

	"github.com/hanzoai/authz"

	"github.com/hanzoai/reward/engine"
)

// link has the platform report that subject signed up through sponsor, and says what
// ingest should answer.
func (r *rig) link(subject, sponsor string, want int) {
	r.t.Helper()
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "link-" + subject + "-" + sponsor, "type": "referral.linked", "subject": subject,
		"fields": map[string]any{"sponsor": sponsor}}, want, nil)
}

// fix has subject's merged fix, 75 points under the seed, claimed and approved. It
// returns the claim.
func (r *rig) fix(id, subject string) string {
	r.t.Helper()
	var out engine.Outcome
	r.json(staff, "POST", pub+"/events", map[string]any{"id": id, "type": "github.pr.merged", "subject": subject,
		"fields":   map[string]any{"repo": "hanzoai/commerce", "labels": []string{}, "kind": "fix"},
		"evidence": map[string]any{"source": "github", "url": "https://github.com/hanzoai/x", "needs_review": true}}, 200, &out)
	for _, d := range out.Decisions {
		if d.Result == "claimed" && d.Rule == "pr-fix" {
			r.json(staff, "POST", admin+"/claims/"+d.Claim.Id()+"/approve", nil, 200, nil)
			return d.Claim.Id()
		}
	}
	r.t.Fatalf("no claim for %s: %+v", id, out)
	return ""
}

func TestIngestRefusesASelfSponsorAndACycleWith400(t *testing.T) {
	r := newRig(t)
	r.seeded()
	r.link("a", "a", 400)
	r.link("b", "a", 200)
	r.link("a", "b", 400)
	// a second link for b, to anyone, is not an error.
	r.link("b", "c", 200)
	r.link("c", "b", 200)
	r.link("d", "c", 200)
	r.link("a", "d", 400)
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "x", "type": "referral.linked", "subject": "e", "fields": map[string]any{}}, 400, nil)
	r.json(staff, "POST", pub+"/events", map[string]any{"id": "y", "type": "referral.linked", "subject": "e", "fields": map[string]any{"sponsor": ""}}, 400, nil)

	code, body, _ := r.do(nobody, "GET", pub+"/catalog", nil)
	if code != 200 || !strings.Contains(string(body), `"referral.linked"`) {
		t.Errorf("the catalog does not list referral.linked: %d", code)
	}
}

// network is the part of /me this test reads.
type network struct {
	Level1 int   `json:"level1"`
	Level2 int   `json:"level2"`
	Level3 int   `json:"level3"`
	Points int64 `json:"points"`
}

func TestMeShowsTheNetworkAndTheUplinePointsAndNoPublicViewDoes(t *testing.T) {
	r := newRig(t)
	r.seeded()
	// s1 brought in m1 and m2; m1 brought m3; m3 brought m4; m4 brought m5.
	r.link("m1", "s1", 200)
	r.link("m2", "s1", 200)
	r.link("m3", "m1", 200)
	r.link("m4", "m3", 200)
	r.link("m5", "m4", 200)
	r.fix("pr-m3", "m3") // pays m1 7 and s1 3
	last := r.fix("pr-m4", "m4")

	type me struct {
		Programs []struct {
			Lifetime   int64    `json:"lifetime"`
			Redeemable int64    `json:"redeemable"`
			Network    *network `json:"network"`
		} `json:"programs"`
	}
	s1 := who{authz.HeaderUser: "s1"}
	var got me
	r.json(s1, "GET", pub+"/me", nil, 200, &got)
	if len(got.Programs) != 1 || got.Programs[0].Network == nil {
		t.Fatalf("me = %+v", got)
	}
	// m4's fix pays m3 7, m1 3 and s1 1.
	if p, n := got.Programs[0], got.Programs[0].Network; *n != (network{2, 1, 1, 4}) || p.Lifetime != 4 || p.Redeemable != 0 {
		t.Errorf("s1 = %+v network %+v, want 2, 1, 1 and 4 points, all lifetime", p, *n)
	}

	var mid me
	r.json(who{authz.HeaderUser: "m1"}, "GET", pub+"/me", nil, 200, &mid)
	if n := mid.Programs[0].Network; n.Level1 != 1 || n.Level2 != 1 || n.Level3 != 1 || n.Points != 10 {
		t.Errorf("m1 network = %+v, want 1, 1, 1 and 10 points", *n)
	}
	var none me
	r.json(bob, "GET", pub+"/me", nil, 200, &none)
	if n := none.Programs[0].Network; n == nil || *n != (network{}) {
		t.Errorf("a member nobody brought in = %+v, want zeros", n)
	}

	// a reversal takes the points back and leaves the network.
	r.json(staff, "POST", admin+"/claims/"+last+"/reverse", map[string]string{"note": "fraud"}, 200, nil)
	r.json(s1, "GET", pub+"/me", nil, 200, &got)
	if n := got.Programs[0].Network; n.Level1 != 2 || n.Level2 != 1 || n.Level3 != 1 || n.Points != 3 {
		t.Errorf("s1 network after the reversal = %+v, want 2, 1, 1 and 3 points", *n)
	}

	// neither the public profile nor the badge shows it.
	r.json(s1, "PUT", pub+"/me/profile", map[string]any{"handle": "sponsor", "public": true}, 200, nil)
	for _, path := range []string{pub + "/profile/sponsor", pub + "/badge/sponsor.svg"} {
		code, body, _ := r.do(nobody, "GET", path, nil)
		if code != 200 {
			t.Fatalf("GET %s = %d", path, code)
		}
		if s := strings.ToLower(string(body)); strings.Contains(s, "network") || strings.Contains(s, "level1") || strings.Contains(s, "upline") {
			t.Errorf("GET %s shows the network: %s", path, body)
		}
	}
}

func TestUplineIsEditedWithTheProgramHeader(t *testing.T) {
	r := newRig(t)
	r.seeded()
	var p map[string]any
	r.json(staff, "GET", admin+"/programs/hanzo-rewards", nil, 200, &p)
	if got := p["upline"]; len(got.([]any)) != 3 {
		t.Fatalf("seeded upline = %v", got)
	}
	delete(p, "rules")
	for _, upline := range [][]int{{10, 5, 2, 1}, {101}} {
		p["upline"] = upline
		r.json(staff, "PUT", admin+"/programs/hanzo-rewards", p, 400, nil)
	}
	p["upline"] = []int{20, 0, 0}
	var saved struct {
		Upline []int `json:"upline"`
	}
	r.json(staff, "PUT", admin+"/programs/hanzo-rewards", p, 200, &saved)
	if len(saved.Upline) != 3 || saved.Upline[0] != 20 {
		t.Errorf("saved = %+v", saved)
	}
	r.json(staff, "GET", admin+"/programs/hanzo-rewards", nil, 200, &saved)
	if len(saved.Upline) != 3 || saved.Upline[0] != 20 {
		t.Errorf("read back = %+v", saved)
	}
}
