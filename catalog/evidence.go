package catalog

import (
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/reward/refuse"
)

// Evidence is what staff read to verify a happening that took place on someone
// else's site: where it is, and what the host's own automated checks found there.
//
// A host states only checks it ran. A source it cannot read (LinkedIn without the
// member's consent, a page behind a login) arrives with NeedsReview and no
// checks, and staff verify it by hand. Evidence rides the event onto the claim, so
// the queue shows it beside the claim it decides.
type Evidence struct {
	// Source is the network or site the happening took place on: github, x,
	// bluesky, reddit, hn, mastodon, linkedin, youtube.
	Source string `json:"source"`
	// URL is the canonical https URL of the post, pull request, repository or video.
	URL string `json:"url"`
	// NeedsReview marks a source the host could not read. It carries no checks.
	NeedsReview bool `json:"needs_review,omitempty"`
	// Checks is the history of automated checks, oldest first: the first look and
	// every re-check after it.
	Checks []Check `json:"checks,omitempty"`
	// Recheck is the last re-check of a post a week after it was found, when the
	// event is held until then.
	Recheck *Recheck `json:"recheck,omitempty"`
}

// Check is one automated look at the source.
type Check struct {
	// FetchedAt is when the source was fetched.
	FetchedAt time.Time `json:"fetched_at"`
	// Status is the HTTP status the fetch answered.
	Status int `json:"status"`
	// Public is whether the source is readable by anyone.
	Public bool `json:"public"`
	// RefFound is whether the member's own ref code was found. It is stated only
	// where a ref code is expected, which is a post.
	RefFound *bool `json:"ref_found,omitempty"`
	// AuthorMatch is whether the author is the member's linked identity: yes, no
	// or unknown.
	AuthorMatch string `json:"author_match"`
}

// Recheck is the result of the re-check a post waits for.
type Recheck struct {
	At time.Time `json:"at"`
	// Result is ok (still up, still carrying the link) or removed.
	Result string `json:"result"`
}

// The author verdicts and the re-check results.
const (
	Yes     = "yes"
	No      = "no"
	Unknown = "unknown"

	StillUp = "ok"
	Removed = "removed"
)

const (
	maxURL    = 2048
	maxChecks = 64
)

var sourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// Validate reports why e is not evidence a staff member can read.
func (e *Evidence) Validate() error {
	e.Source, e.URL = strings.TrimSpace(e.Source), strings.TrimSpace(e.URL)
	if !sourceName.MatchString(e.Source) {
		return refuse.New("evidence source must be a short lower-case name such as github or bluesky")
	}
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(e.URL) > maxURL || strings.ContainsAny(e.URL, " \x00\n\r\t") {
		return refuse.New("evidence url must be one https URL of at most %d characters", maxURL)
	}
	if e.NeedsReview {
		if len(e.Checks) > 0 || e.Recheck != nil {
			return refuse.New("evidence that needs review claims no checks")
		}
		return nil
	}
	if len(e.Checks) == 0 {
		return refuse.New("evidence needs at least one check, or needs_review")
	}
	if len(e.Checks) > maxChecks {
		return refuse.New("evidence holds at most %d checks", maxChecks)
	}
	for i, c := range e.Checks {
		switch {
		case c.FetchedAt.IsZero():
			return refuse.New("check %d needs fetched_at", i+1)
		case c.Status < 100 || c.Status > 599:
			return refuse.New("check %d: status %d is not an HTTP status", i+1, c.Status)
		case c.AuthorMatch != Yes && c.AuthorMatch != No && c.AuthorMatch != Unknown:
			return refuse.New("check %d: author_match must be yes, no or unknown", i+1)
		}
	}
	if r := e.Recheck; r != nil {
		if r.At.IsZero() || (r.Result != StillUp && r.Result != Removed) {
			return refuse.New("recheck needs a time and a result of ok or removed")
		}
	}
	return nil
}
