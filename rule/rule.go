// Package rule is what a program is made of: rules that read one event, test its
// fields, and say what an approved claim does; and the program that holds them
// with its budget, levels, badges and email templates.
package rule

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/refuse"
)

// Op compares one event field to a value.
type Op string

const (
	Eq       Op = "eq"
	Ne       Op = "ne"
	In       Op = "in"
	Gte      Op = "gte"
	Lte      Op = "lte"
	Contains Op = "contains"
)

// OpInfo says which kinds of field an Op reads.
type OpInfo struct {
	ID    Op             `json:"id"`
	Kinds []catalog.Kind `json:"kinds"`
}

// ops are the six comparisons and the kinds each one reads. Contains on Text is a
// substring and on Texts is membership; both ignore case.
var ops = []OpInfo{
	{Eq, []catalog.Kind{catalog.Text, catalog.Whole, catalog.Real, catalog.Flag, catalog.Choice}},
	{Ne, []catalog.Kind{catalog.Text, catalog.Whole, catalog.Real, catalog.Flag, catalog.Choice}},
	{In, []catalog.Kind{catalog.Text, catalog.Whole, catalog.Real, catalog.Choice}},
	{Gte, []catalog.Kind{catalog.Whole, catalog.Real}},
	{Lte, []catalog.Kind{catalog.Whole, catalog.Real}},
	{Contains, []catalog.Kind{catalog.Text, catalog.Texts}},
}

// Condition holds when an event's Field compares to Value by Op.
type Condition struct {
	Field string `json:"field"`
	Op    Op     `json:"op"`
	// Value is a JSON scalar, or for In a list of them.
	Value any `json:"value"`
}

// Type names what an Action does.
type Type string

// Do names the four action types.
var Do = struct{ Points, Badge, Credit, Email Type }{
	// Points adds to the subject's points ledger.
	Points: "points",
	// Badge awards the subject a badge of the program.
	Badge: "badge",
	// Credit grants the subject's org credit, through commerce's one credit path.
	Credit: "credit",
	// Email sends the subject a template of the program.
	Email: "email",
}

// ActionInfo describes one Type for a page.
type ActionInfo struct {
	ID   Type     `json:"id"`
	Uses []string `json:"uses"`
}

var actions = []ActionInfo{
	{Do.Points, []string{"points", "redeemable"}},
	{Do.Badge, []string{"badge"}},
	{Do.Credit, []string{"cents"}},
	{Do.Email, []string{"template"}},
}

// Action is one thing an approved claim does. Only the fields of its Type are read.
type Action struct {
	Type Type `json:"type"`
	// Points is what a points action adds; Redeemable says whether those points
	// count toward the subject's redeemable balance as well as their lifetime one.
	Points     int64 `json:"points,omitempty"`
	Redeemable bool  `json:"redeemable,omitempty"`
	// Badge is the id of a badge of the program.
	Badge string `json:"badge,omitempty"`
	// Cents is what a credit action grants, in cents.
	Cents int64 `json:"cents,omitempty"`
	// Template is the id of a template of the program.
	Template string `json:"template,omitempty"`
}

// Approval is who verifies an award.
type Approval string

const (
	// Manual puts the claim in the staff queue; nothing is awarded until a
	// person approves it. It is the default.
	Manual Approval = "manual"
	// Auto approves the claim as it is created.
	Auto Approval = "auto"
)

var approvals = []Approval{Manual, Auto}

// Once bounds how often one subject earns a rule at all.
type Once string

const (
	// Unlimited puts no such bound.
	Unlimited Once = ""
	// Subject lets a subject earn the rule once.
	Subject Once = "subject"
	// Keyed lets a subject earn the rule once per value of Limits.KeyField.
	Keyed Once = "subject+key"
)

var onces = []Once{Unlimited, Subject, Keyed}

// Limits bound a rule per subject. A claim counts toward them while it is
// pending, approved or awarded; one rejected or reversed does not.
type Limits struct {
	Once Once `json:"once,omitempty"`
	// KeyField is the event field Keyed reads.
	KeyField string `json:"keyField,omitempty"`
	// PerDay and PerMonth are the most claims of the rule one subject may have
	// in one UTC day and in one UTC calendar month. 0 is no bound.
	PerDay   int `json:"perDay,omitempty"`
	PerMonth int `json:"perMonth,omitempty"`
}

// When names the one event type a rule reads.
type When struct {
	Event string `json:"event"`
}

// Rule is one way to earn: when an event of When happens and every If holds, the
// Then actions are done once a claim for it is approved.
type Rule struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Pillar catalog.Pillar `json:"pillar"`
	Active bool           `json:"active"`
	// Version starts at 1 and is raised by the store each time the rule changes.
	// A claim is made under one version and awards what that version said.
	Version  int         `json:"version"`
	Approval Approval    `json:"approval"`
	When     When        `json:"when"`
	If       []Condition `json:"if"`
	Then     []Action    `json:"then"`
	Limits   Limits      `json:"limits"`
}

// Words is the vocabulary a page builds a rule editor from.
type Words struct {
	Ops       []OpInfo     `json:"ops"`
	Actions   []ActionInfo `json:"actions"`
	Approvals []Approval   `json:"approvals"`
	Onces     []Once       `json:"onces"`
}

// Vocabulary is every op, action type, approval and once there is.
func Vocabulary() Words {
	return Words{
		Ops:       append([]OpInfo(nil), ops...),
		Actions:   append([]ActionInfo(nil), actions...),
		Approvals: append([]Approval(nil), approvals...),
		Onces:     append([]Once(nil), onces...),
	}
}

// Slug is what an id of a program, rule, badge or template must look like.
var Slug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Validate reports why r cannot be a rule of p, or nil. It also settles what an
// empty field means: an empty Approval is Manual, an empty Pillar is its event's.
func (r *Rule) Validate(p *Program) error {
	if !Slug.MatchString(r.ID) {
		return refuse.New("rule id %q must be 1-64 of a-z, 0-9, '.', '_' and '-'", r.ID)
	}
	if strings.TrimSpace(r.Name) == "" {
		return refuse.New("rule %s needs a name", r.ID)
	}
	ev, ok := catalog.Type(r.When.Event)
	if !ok {
		return refuse.New("rule %s: %q is not an event in the catalog", r.ID, r.When.Event)
	}
	if r.Pillar == "" {
		r.Pillar = ev.Pillar
	}
	if !r.Pillar.Valid() {
		return refuse.New("rule %s: %q is not a pillar", r.ID, r.Pillar)
	}
	switch r.Approval {
	case "":
		r.Approval = Manual
	case Manual, Auto:
	default:
		return refuse.New("rule %s: approval must be manual or auto", r.ID)
	}
	if r.If == nil {
		r.If = []Condition{}
	}
	for _, c := range r.If {
		if err := c.validate(ev); err != nil {
			return refuse.New("rule %s: %v", r.ID, err)
		}
	}
	if len(r.Then) == 0 {
		return refuse.New("rule %s: then needs at least one action", r.ID)
	}
	for _, a := range r.Then {
		if err := a.validate(p); err != nil {
			return refuse.New("rule %s: %v", r.ID, err)
		}
	}
	switch r.Limits.Once {
	case Unlimited, Subject:
	case Keyed:
		if _, ok := ev.Field(r.Limits.KeyField); !ok {
			return refuse.New("rule %s: once subject+key needs keyField to name a field of %s", r.ID, ev.Name)
		}
	default:
		return refuse.New("rule %s: once must be empty, subject or subject+key", r.ID)
	}
	if r.Limits.PerDay < 0 || r.Limits.PerMonth < 0 {
		return refuse.New("rule %s: perDay and perMonth must not be negative", r.ID)
	}
	return nil
}

func (c Condition) validate(ev catalog.EventType) error {
	f, ok := ev.Field(c.Field)
	if !ok {
		return fmt.Errorf("%q is not a field of %s", c.Field, ev.Name)
	}
	var kinds []catalog.Kind
	for _, o := range ops {
		if o.ID == c.Op {
			kinds = o.Kinds
		}
	}
	if kinds == nil {
		return fmt.Errorf("%q is not an op", c.Op)
	}
	ok = false
	for _, k := range kinds {
		ok = ok || k == f.Kind
	}
	if !ok {
		return fmt.Errorf("%s cannot be applied to %s, a %s", c.Op, f.Name, f.Kind)
	}
	if c.Op == In {
		list, isList := c.Value.([]any)
		if !isList || len(list) == 0 {
			return fmt.Errorf("in needs a list of values for %s", f.Name)
		}
		for _, v := range list {
			if _, err := f.Coerce(v); err != nil {
				return fmt.Errorf("%s: %v", f.Name, err)
			}
		}
		return nil
	}
	vk := f
	if c.Op == Contains {
		vk = catalog.Field{Name: f.Name, Kind: catalog.Text}
	}
	if _, err := vk.Coerce(c.Value); err != nil {
		return fmt.Errorf("%s: %v", f.Name, err)
	}
	return nil
}

func (a Action) validate(p *Program) error {
	switch a.Type {
	case Do.Points:
		if a.Points < 0 {
			return fmt.Errorf("points must not be negative")
		}
	case Do.Badge:
		if a.Badge == "" {
			return fmt.Errorf("badge needs an id")
		}
		if p != nil && p.Badge(a.Badge) == nil {
			return fmt.Errorf("badge %q is not a badge of the program", a.Badge)
		}
	case Do.Credit:
		if a.Cents <= 0 {
			return fmt.Errorf("credit needs cents above zero")
		}
	case Do.Email:
		if a.Template == "" {
			return fmt.Errorf("email needs a template")
		}
		if p != nil && p.Template(a.Template) == nil {
			return fmt.Errorf("template %q is not a template of the program", a.Template)
		}
	default:
		return fmt.Errorf("%q is not an action type", a.Type)
	}
	return nil
}

// Template is the text of one email: Go text/template over the claim.
type Template struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (t Template) validate() error {
	if !Slug.MatchString(t.ID) {
		return refuse.New("template id %q must be 1-64 of a-z, 0-9, '.', '_' and '-'", t.ID)
	}
	for _, s := range []string{t.Subject, t.Body} {
		if _, err := template.New(t.ID).Parse(s); err != nil {
			return refuse.New("template %s: %v", t.ID, err)
		}
	}
	return nil
}
