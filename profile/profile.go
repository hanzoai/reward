// Package profile is what a subject chooses to show: a public handle, whether their
// standing is public at all, and the links they give.
//
// A profile is private until its owner turns it on, and then shows level, points,
// badges and links and nothing else: never an email, never a redeemable balance,
// never the subject id. The owner reads and writes their own through the self route;
// the public reads by handle.
package profile

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/refuse"
)

// Kinds in the store.
const (
	Kind       = "reward-profile"
	HandleKind = "reward-handle"
)

func init() {
	orm.Register[Profile](Kind, orm.WithStringKey[Profile]())
	orm.Register[Handle](HandleKind, orm.WithStringKey[Handle]())
}

// Links are the member-provided links, each an https URL or empty.
type Links struct {
	GitHub  string `json:"github,omitempty"`
	X       string `json:"x,omitempty"`
	Website string `json:"website,omitempty"`
}

// Profile is one subject's, under the subject as its id.
type Profile struct {
	orm.Model[Profile]

	// Handle is the public name, unique and lower case. Empty until chosen.
	Handle string `json:"handle"`
	// Public says whether anyone may read the standing at Handle. False by default.
	Public bool  `json:"public"`
	Links  Links `json:"links"`
}

// Handle holds a handle for the subject who chose it, under the handle as its id: a
// handle is taken by the conditional insert of this row and by nothing else.
type Handle struct {
	orm.Model[Handle]

	Subject string `json:"subject"`
}

// ProfileID and HandleID are the store ids of a subject's profile and of a handle.
// Every kind keeps ids in a keyspace of its own, because the store's id column is
// one key for all kinds: a subject named alice and a handle named alice are two rows.
func ProfileID(subject string) string { return "p:" + subject }

// HandleID is the store id of a handle.
func HandleID(handle string) string { return "h:" + handle }

var handle = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,31}$`)

// Normalize lower-cases and trims a handle.
func Normalize(h string) string { return strings.ToLower(strings.TrimSpace(h)) }

// Validate reports why p is not a profile that can be saved. A public profile needs
// a handle.
func (p *Profile) Validate() error {
	p.Handle = Normalize(p.Handle)
	if p.Handle != "" && !handle.MatchString(p.Handle) {
		return refuse.New("a handle is 3 to 32 of a-z, 0-9, '_' and '-', starting with a letter or number")
	}
	if p.Public && p.Handle == "" {
		return refuse.New("a public profile needs a handle")
	}
	for name, l := range map[string]*string{"github": &p.Links.GitHub, "x": &p.Links.X, "website": &p.Links.Website} {
		*l = strings.TrimSpace(*l)
		if err := checkLink(*l); err != nil {
			return refuse.New("%s link: %v", name, err)
		}
	}
	return nil
}

func checkLink(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > 200 {
		return refuse.New("at most 200 characters")
	}
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return refuse.New("not a URL")
	case u.Scheme != "https":
		return refuse.New("must be an https URL")
	case u.Host == "" || u.Hostname() == "":
		return refuse.New("needs a host")
	case u.User != nil:
		return refuse.New("must not carry a login")
	}
	return nil
}
