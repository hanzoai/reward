package catalog

import (
	"testing"
	"time"

	"github.com/hanzoai/reward/refuse"
)

func checked() *Evidence {
	yes := true
	return &Evidence{Source: "bluesky", URL: "https://bsky.app/profile/a.bsky.social/post/3k",
		Checks:  []Check{{FetchedAt: time.Now(), Status: 200, Public: true, RefFound: &yes, AuthorMatch: Yes}},
		Recheck: &Recheck{At: time.Now(), Result: StillUp}}
}

func post(ev *Evidence) Event {
	return Event{ID: "p1", Type: "social.post.verified", Subject: "u", Evidence: ev,
		Fields: map[string]any{"network": "bluesky", "url": "https://bsky.app/profile/a.bsky.social/post/3k", "product": "", "model": "", "week": "2026-W46"}}
}

func TestAThirdPartyEventIsRefusedWithoutEvidence(t *testing.T) {
	for _, name := range []string{"social.post.verified", "github.star", "github.follow", "github.fork", "github.badge", "github.issue.accepted", "github.pr.merged", "bounty.completed"} {
		et, _ := Type(name)
		if !et.Evidence {
			t.Errorf("%s does not ask for evidence", name)
		}
	}
	e := post(nil)
	if err := e.Validate(); !refuse.Is(err) {
		t.Errorf("a post with no evidence: %v, want a refusal", err)
	}
	e = post(checked())
	if err := e.Validate(); err != nil {
		t.Errorf("a post with checks: %v", err)
	}
	// an event on our own side asks for none.
	tok := Event{ID: "t", Type: "usage.tokens", Subject: "u", Fields: map[string]any{"tokens": 100000, "model": "zen4"}}
	if err := tok.Validate(); err != nil {
		t.Errorf("usage.tokens: %v", err)
	}
}

func TestEvidenceThatNeedsReviewClaimsNoChecks(t *testing.T) {
	e := post(&Evidence{Source: "linkedin", URL: "https://www.linkedin.com/posts/a_1-activity-1", NeedsReview: true})
	if err := e.Validate(); err != nil {
		t.Fatalf("an unreadable source for review: %v", err)
	}
	e = post(&Evidence{Source: "linkedin", URL: "https://www.linkedin.com/posts/a_1-activity-1", NeedsReview: true, Checks: checked().Checks})
	if err := e.Validate(); !refuse.Is(err) {
		t.Errorf("needs_review with checks: %v, want a refusal", err)
	}
	e = post(&Evidence{Source: "linkedin", URL: "https://www.linkedin.com/posts/a_1-activity-1", NeedsReview: true, Recheck: &Recheck{At: time.Now(), Result: StillUp}})
	if err := e.Validate(); !refuse.Is(err) {
		t.Errorf("needs_review with a recheck: %v, want a refusal", err)
	}
	e = post(&Evidence{Source: "bluesky", URL: "https://bsky.app/profile/a/post/1"})
	if err := e.Validate(); !refuse.Is(err) {
		t.Errorf("no checks and no needs_review: %v, want a refusal", err)
	}
}

func TestEvidenceIsAReadableRecordOrRefused(t *testing.T) {
	bad := map[string]func(*Evidence){
		"http url":        func(e *Evidence) { e.URL = "http://bsky.app/x" },
		"credentials":     func(e *Evidence) { e.URL = "https://u:p@bsky.app/x" },
		"spaces":          func(e *Evidence) { e.URL = "https://bsky.app/a b" },
		"no source":       func(e *Evidence) { e.Source = "" },
		"loud source":     func(e *Evidence) { e.Source = "Bluesky!" },
		"no fetched_at":   func(e *Evidence) { e.Checks[0].FetchedAt = time.Time{} },
		"status 0":        func(e *Evidence) { e.Checks[0].Status = 0 },
		"status 700":      func(e *Evidence) { e.Checks[0].Status = 700 },
		"author maybe":    func(e *Evidence) { e.Checks[0].AuthorMatch = "maybe" },
		"recheck unknown": func(e *Evidence) { e.Recheck.Result = "gone" },
		"recheck no time": func(e *Evidence) { e.Recheck.At = time.Time{} },
	}
	for name, mod := range bad {
		ev := checked()
		mod(ev)
		e := post(ev)
		if err := e.Validate(); !refuse.Is(err) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
	// the history of a week is kept whole, oldest first.
	ev := checked()
	for range 6 {
		ev.Checks = append(ev.Checks, ev.Checks[0])
	}
	e := post(ev)
	if err := e.Validate(); err != nil || len(e.Evidence.Checks) != 7 {
		t.Errorf("a week of checks: %v, %d", err, len(e.Evidence.Checks))
	}
	for range 60 {
		ev.Checks = append(ev.Checks, ev.Checks[0])
	}
	e = post(ev)
	if err := e.Validate(); !refuse.Is(err) {
		t.Errorf("more checks than a history holds: %v", err)
	}
}
