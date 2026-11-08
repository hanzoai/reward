// Package catalog is the typed list of everything that can happen: the events a
// rule may read, the fields each carries, and the pillar each belongs to.
//
// The catalog is code. An event is a typed addition here, never a free string a
// caller invents: a rule names one of these, and an ingested event is refused
// unless it carries exactly the fields listed. The admin UI builds its dropdowns
// from Lookup.
package catalog

import "slices"

// Pillar is the part of the program a rule or a badge belongs to. A page groups
// by it, and the catalog lists every event under one.
type Pillar string

const (
	// Use is signing up and using Hanzo: its models and its products.
	Use Pillar = "use"
	// Share is telling friends.
	Share Pillar = "share"
	// Community is joining the community.
	Community Pillar = "community"
	// Contribute is open source and the ecosystem.
	Contribute Pillar = "contribute"
	// Expert is human experts on hanzo.market.
	Expert Pillar = "expert"
)

// PillarInfo names a pillar for a page.
type PillarInfo struct {
	ID    Pillar `json:"id"`
	Title string `json:"title"`
	About string `json:"about"`
}

// pillars are the five, in the order a page draws them.
var pillars = []PillarInfo{
	{Use, "Use", "Sign up and use Hanzo: models, products."},
	{Share, "Share", "Tell friends."},
	{Community, "Community", "Join the community."},
	{Contribute, "Contribute", "Open source and the ecosystem."},
	{Expert, "Expert", "Human experts on hanzo.market."},
}

// Valid reports whether p is one of the five pillars.
func (p Pillar) Valid() bool {
	for _, x := range pillars {
		if x.ID == p {
			return true
		}
	}
	return false
}

// Kind is the type of one event field.
type Kind string

const (
	// Text is a string compared whole.
	Text Kind = "text"
	// Whole is an integer.
	Whole Kind = "whole"
	// Real is any number.
	Real Kind = "real"
	// Flag is a boolean.
	Flag Kind = "flag"
	// Texts is a list of strings, such as a pull request's labels.
	Texts Kind = "texts"
	// Choice is one of a fixed set of strings, Values.
	Choice Kind = "choice"
)

// Field is one typed value an event carries.
type Field struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Values are the strings a Choice may take.
	Values []string `json:"values,omitempty"`
}

// EventType is one kind of thing that happened, and the fields it carries.
type EventType struct {
	// Name is the event's type, such as github.pr.merged.
	Name   string `json:"name"`
	Pillar Pillar `json:"pillar"`
	// Title is the event as a dropdown names it; About says when it is emitted.
	Title  string  `json:"title"`
	About  string  `json:"about"`
	Fields []Field `json:"fields"`
	// Evidence is true for an event that takes place on someone else's site: the
	// host must state where, and what its checks found, with every one.
	Evidence bool `json:"evidence,omitempty"`
}

func text(n string) Field                { return Field{Name: n, Kind: Text} }
func texts(n string) Field               { return Field{Name: n, Kind: Texts} }
func flag(n string) Field                { return Field{Name: n, Kind: Flag} }
func whole(n string) Field               { return Field{Name: n, Kind: Whole} }
func number(n string) Field              { return Field{Name: n, Kind: Real} }
func choice(n string, v ...string) Field { return Field{Name: n, Kind: Choice, Values: v} }

// Linked is the event that attributes a member to a sponsor. The engine reads it
// itself, before any rule does.
const Linked = "referral.linked"

// Welcomed is the event that thanks a referred person for their first real task.
const Welcomed = "referral.welcomed"

// events is THE list.
var events = []EventType{
	{Name: "usage.tokens", Pillar: Use, Title: "Tokens used",
		About:  "A subject's verified usage grew. tokens is their lifetime total at the time; model is the canonical id of the model that took them to it.",
		Fields: []Field{whole("tokens"), text("model")}},
	{Name: "product.used", Pillar: Use, Title: "Product used",
		About:  "A subject's first real use of a product. product is the product's slug.",
		Fields: []Field{text("product")}},
	{Name: "model.used", Pillar: Use, Title: "Model used",
		About:  "A subject's first successful task on a model. model is the CANONICAL model id: an alias is not a model, so it never arrives here.",
		Fields: []Field{text("model")}},

	{Name: "referral.activated", Pillar: Share, Title: "Referral activated",
		About: "A person a subject referred ran their first real task."},
	{Name: "referral.retained", Pillar: Share, Title: "Referral retained",
		About: "A person a subject referred came back at least seven days after they activated."},
	{Name: Linked, Pillar: Share, Title: "Referral linked",
		About:  "A person's sign-up was attributed to a sponsor.",
		Fields: []Field{text("sponsor")}},
	{Name: Welcomed, Pillar: Share, Title: "Referred person welcomed",
		About:  "A person someone referred ran their first real task. The event is about the referred person; sponsor is the person who referred them.",
		Fields: []Field{text("sponsor")}},
	{Name: "social.post.verified", Pillar: Share, Title: "Post verified",
		About:  "A subject's public post carrying their link was fetched and found still up a week later. week is the ISO year-week of the post's publication, such as 2026-W46, so a later week's post can earn again.",
		Fields: []Field{text("network"), text("url"), text("product"), text("model"), text("week")}, Evidence: true},

	{Name: "discord.joined", Pillar: Community, Title: "Discord joined",
		About: "A subject's Discord account joined and was linked to their account."},
	{Name: "github.star", Pillar: Community, Title: "Repository starred",
		About:  "A subject starred a repository, checked on GitHub against their linked account.",
		Fields: []Field{text("repo")}, Evidence: true},
	{Name: "github.follow", Pillar: Community, Title: "Followed on GitHub",
		About:  "A subject followed an account on GitHub. target is that account.",
		Fields: []Field{text("target")}, Evidence: true},

	{Name: "github.fork", Pillar: Contribute, Title: "Repository forked",
		About:  "A subject forked a repository of ours. forker is the GitHub account that forked it.",
		Fields: []Field{text("repo"), text("forker")}, Evidence: true},
	{Name: "github.badge", Pillar: Contribute, Title: "Hanzo badge in a README",
		About:  "A subject's public project links Hanzo from its README. Emitted once a verifier has confirmed the repository is public, its owner is the subject's linked GitHub identity, and its README carries the badge. badge is which one: profile (the live badge of their rewards profile), built (the static Built with Hanzo badge), rank (the live rank badge of their org) or deploy (a Deploy to Hanzo button).",
		Fields: []Field{text("repo"), text("owner"), choice("badge", "profile", "built", "rank", "deploy")}, Evidence: true},
	{Name: "github.issue.accepted", Pillar: Contribute, Title: "Issue accepted",
		About:  "A maintainer accepted an issue a subject opened.",
		Fields: []Field{text("repo"), texts("labels")}, Evidence: true},
	{Name: "github.pr.merged", Pillar: Contribute, Title: "Pull request merged",
		About:  "A maintainer merged a subject's pull request into a public repository.",
		Fields: []Field{text("repo"), texts("labels"), choice("kind", "fix", "feature", "docs", "tests", "translation")}, Evidence: true},
	{Name: "submission.video", Pillar: Contribute, Title: "Demo video submitted",
		About:  "A subject's demo video of an app was accepted.",
		Fields: []Field{choice("app", "cli", "desktop", "mobile"), text("url")}, Evidence: true},
	{Name: "submission.test", Pillar: Contribute, Title: "Test report submitted",
		About:  "A subject's test report on a product was accepted. beta is whether the product is in beta.",
		Fields: []Field{text("product"), flag("beta")}},
	{Name: "benchmark.added", Pillar: Contribute, Title: "Benchmark added",
		About:  "A subject's benchmark was added to a suite. pr is the pull request that added it.",
		Fields: []Field{text("suite"), text("pr")}},
	{Name: "bounty.completed", Pillar: Contribute, Title: "Bounty completed",
		About:  "A subject completed a bounty on an issue, of a tier.",
		Fields: []Field{text("repo"), text("issue"), choice("tier", "starter", "standard", "advanced")}, Evidence: true},
	{Name: "deploy.button", Pillar: Contribute, Title: "Deploy button added",
		About:  "A subject's repository added a Deploy on Hanzo button.",
		Fields: []Field{text("repo"), text("author")}, Evidence: true},

	{Name: "expert.listed", Pillar: Expert, Title: "Expert listed",
		About:  "A subject listed themselves as an expert on hanzo.market.",
		Fields: []Field{text("listing")}},
	{Name: "expert.verified", Pillar: Expert, Title: "Expert verified",
		About:  "An expert's listing was verified.",
		Fields: []Field{text("listing")}},
	{Name: "expert.job.delivered", Pillar: Expert, Title: "Job delivered",
		About:  "An expert delivered a job. rating is the client's rating of it.",
		Fields: []Field{text("job"), number("rating")}},
	{Name: "expert.review", Pillar: Expert, Title: "Job reviewed",
		About:  "A client reviewed a job an expert delivered.",
		Fields: []Field{text("job"), number("rating")}},
}

// Group is a pillar and the events listed under it.
type Group struct {
	PillarInfo
	Events []EventType `json:"events"`
}

// Lookup is the catalog grouped by pillar, in the pillars' order.
func Lookup() []Group {
	out := make([]Group, 0, len(pillars))
	for _, p := range pillars {
		g := Group{PillarInfo: p, Events: []EventType{}}
		for _, e := range events {
			if e.Pillar == p.ID {
				g.Events = append(g.Events, e)
			}
		}
		out = append(out, g)
	}
	return out
}

// Pillars lists the five pillars in order.
func Pillars() []PillarInfo { return slices.Clone(pillars) }

// Events lists every event type, in catalog order.
func Events() []EventType { return slices.Clone(events) }

// Type returns the catalog entry named name.
func Type(name string) (EventType, bool) {
	for _, e := range events {
		if e.Name == name {
			return e, true
		}
	}
	return EventType{}, false
}

// Field returns the field of e named name.
func (e EventType) Field(name string) (Field, bool) {
	for _, f := range e.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}
