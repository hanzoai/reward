package rule

import (
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/refuse"
)

// Kind is the program's kind in the store.
const Kind = "reward-program"

func init() { orm.Register[Program](Kind, orm.WithStringKey[Program]()) }

// Budget bounds what a program spends in one UTC calendar month, counted from the
// claims made in it that are not rejected or reversed. 0 is no bound.
type Budget struct {
	PointsPerMonth      int64 `json:"pointsPerMonth"`
	CreditCentsPerMonth int64 `json:"creditCentsPerMonth"`
}

// Level is a standing, reached at MinLifetimePoints.
type Level struct {
	// Level is the standing's number, from 1.
	Level             int    `json:"level"`
	Name              string `json:"name"`
	MinLifetimePoints int64  `json:"minLifetimePoints"`
}

// Rarity is how rare a badge is, for the page that shows it.
type Rarity string

const (
	Common    Rarity = "common"
	Uncommon  Rarity = "uncommon"
	Rare      Rarity = "rare"
	Epic      Rarity = "epic"
	Legendary Rarity = "legendary"
)

// Rarities lists the five, commonest first.
var Rarities = []Rarity{Common, Uncommon, Rare, Epic, Legendary}

// Rank is the rarity's place in Rarities; an unknown rarity ranks as common.
func (r Rarity) Rank() int {
	for i, x := range Rarities {
		if x == r {
			return i
		}
	}
	return 0
}

// Badge is a badge a program can award.
type Badge struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Pillar catalog.Pillar `json:"pillar"`
	// Rarity is empty for common.
	Rarity Rarity `json:"rarity,omitempty"`
}

// Program is the set of rules that decide who earns what, the budget they spend,
// the levels points reach and the badges they award. The store holds one document
// per program, under its slug as the id; admin.hanzo.ai edits it, and the seed
// only creates it.
type Program struct {
	orm.Model[Program]

	Name string `json:"name"`
	// Active programs are evaluated; an inactive one awards nothing.
	Active bool `json:"active"`
	// Version starts at 1 and is raised each time the program or one of its rules
	// changes.
	Version int `json:"version"`

	Budget    Budget     `json:"budget"`
	Levels    []Level    `json:"levels"`
	Badges    []Badge    `json:"badges"`
	Templates []Template `json:"templates"`
	Rules     []Rule     `json:"rules"`
}

// ProgramID is the store id of the program slug. Every kind keeps ids in a keyspace
// of its own, because the store's id column is one key for all kinds.
func ProgramID(slug string) string { return "prg:" + slug }

// View is a program as callers see it: its slug as the id, not the store's.
type View struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Active    bool       `json:"active"`
	Version   int        `json:"version"`
	Budget    Budget     `json:"budget"`
	Levels    []Level    `json:"levels"`
	Badges    []Badge    `json:"badges"`
	Templates []Template `json:"templates"`
	Rules     []Rule     `json:"rules"`
	CreatedAt time.Time  `json:"createdAt,omitzero"`
	UpdatedAt time.Time  `json:"updatedAt,omitzero"`
}

// View is p as callers see it.
func (p *Program) View() View {
	return View{ID: p.Slug(), Name: p.Name, Active: p.Active, Version: p.Version, Budget: p.Budget,
		Levels: nonNil(p.Levels), Badges: nonNil(p.Badges), Templates: nonNil(p.Templates), Rules: nonNil(p.Rules),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Slug is the program's id as callers know it.
func (p *Program) Slug() string { return strings.TrimPrefix(p.Id_, "prg:") }

// Find returns the rule with id, or nil.
func (p *Program) Find(id string) *Rule {
	for i := range p.Rules {
		if p.Rules[i].ID == id {
			return &p.Rules[i]
		}
	}
	return nil
}

// Badge returns the badge with id, or nil.
func (p *Program) Badge(id string) *Badge {
	for i := range p.Badges {
		if p.Badges[i].ID == id {
			return &p.Badges[i]
		}
	}
	return nil
}

// Template returns the template with id, or nil.
func (p *Program) Template(id string) *Template {
	for i := range p.Templates {
		if p.Templates[i].ID == id {
			return &p.Templates[i]
		}
	}
	return nil
}

// LevelOf is the highest level that lifetime points reach, and the next one.
func (p *Program) LevelOf(lifetime int64) (Level, *Level) {
	var at Level
	for i, l := range p.Levels {
		if lifetime >= l.MinLifetimePoints {
			at = l
			continue
		}
		next := p.Levels[i]
		return at, &next
	}
	return at, nil
}

// Validate reports why p is not a program: its header and every rule in it.
func (p *Program) Validate() error {
	if !Slug.MatchString(p.Slug()) {
		return refuse.New("program id %q must be 1-64 of a-z, 0-9, '.', '_' and '-'", p.Slug())
	}
	if strings.TrimSpace(p.Name) == "" {
		return refuse.New("program %s needs a name", p.Slug())
	}
	if p.Budget.PointsPerMonth < 0 || p.Budget.CreditCentsPerMonth < 0 {
		return refuse.New("a budget must not be negative; 0 is no bound")
	}
	for i, l := range p.Levels {
		switch {
		case l.Level != i+1:
			return refuse.New("levels must be numbered 1, 2, 3 in order; %q is %d", l.Name, l.Level)
		case strings.TrimSpace(l.Name) == "":
			return refuse.New("level %d needs a name", l.Level)
		case i == 0 && l.MinLifetimePoints != 0:
			return refuse.New("the first level starts at 0 lifetime points")
		case i > 0 && l.MinLifetimePoints <= p.Levels[i-1].MinLifetimePoints:
			return refuse.New("level %d must start above level %d", l.Level, i)
		}
	}
	seen := map[string]bool{}
	for _, b := range p.Badges {
		switch {
		case !Slug.MatchString(b.ID):
			return refuse.New("badge id %q must be 1-64 of a-z, 0-9, '.', '_' and '-'", b.ID)
		case seen[b.ID]:
			return refuse.New("badge %s is listed twice", b.ID)
		case strings.TrimSpace(b.Name) == "":
			return refuse.New("badge %s needs a name", b.ID)
		case !b.Pillar.Valid():
			return refuse.New("badge %s: %q is not a pillar", b.ID, b.Pillar)
		case b.Rarity != "" && !slices.Contains(Rarities, b.Rarity):
			return refuse.New("badge %s: %q is not a rarity", b.ID, b.Rarity)
		}
		seen[b.ID] = true
	}
	seen = map[string]bool{}
	for _, t := range p.Templates {
		if seen[t.ID] {
			return refuse.New("template %s is listed twice", t.ID)
		}
		seen[t.ID] = true
		if err := t.validate(); err != nil {
			return err
		}
	}
	seen = map[string]bool{}
	for i := range p.Rules {
		r := &p.Rules[i]
		if seen[r.ID] {
			return refuse.New("rule %s is listed twice", r.ID)
		}
		seen[r.ID] = true
		if err := r.Validate(p); err != nil {
			return err
		}
	}
	return nil
}
