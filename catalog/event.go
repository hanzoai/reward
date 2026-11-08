package catalog

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hanzoai/reward/refuse"
)

// Event is one thing that happened to one subject, as the caller that saw it
// states it. ID is the caller's own immutable id for the happening: the same ID
// again is the same event, and awards nothing twice.
type Event struct {
	ID string `json:"id"`
	// Type is a name from the catalog.
	Type string `json:"type"`
	// Subject is the IAM principal the event is about: the JWT sub, the same
	// value the subject's own requests carry as X-User-Id.
	Subject string `json:"subject"`
	// Org is the org a credit action lands in. It is required only by a rule
	// that grants credit.
	Org string `json:"org,omitempty"`
	// Email is where an email action goes. It is required only by a rule that
	// sends one.
	Email string `json:"email,omitempty"`
	// At is when it happened; the zero time is unknown. It is kept for the
	// reviewer. Limits and budgets count when the claim was made, not this.
	At time.Time `json:"at,omitzero"`
	// Fields are the values the catalog lists for Type, exactly.
	Fields map[string]any `json:"fields"`
}

const maxText = 256

// Validate reports why e is not an event of the catalog, and settles its Fields
// to the kinds the catalog gives them.
func (e *Event) Validate() error {
	e.ID, e.Subject, e.Org, e.Email = strings.TrimSpace(e.ID), strings.TrimSpace(e.Subject), strings.TrimSpace(e.Org), strings.TrimSpace(e.Email)
	for name, v := range map[string]string{"id": e.ID, "subject": e.Subject, "org": e.Org, "email": e.Email} {
		if len(v) > maxText || strings.ContainsAny(v, "\x00\n\r") {
			return refuse.New("event %s must be at most %d characters on one line", name, maxText)
		}
	}
	if e.ID == "" || e.Subject == "" {
		return refuse.New("event needs an id and a subject")
	}
	ev, ok := Type(e.Type)
	if !ok {
		return refuse.New("%q is not an event in the catalog", e.Type)
	}
	out := make(map[string]any, len(ev.Fields))
	for _, f := range ev.Fields {
		raw, ok := e.Fields[f.Name]
		if !ok {
			return refuse.New("%s needs the field %s", ev.Name, f.Name)
		}
		v, err := f.Coerce(raw)
		if err != nil {
			return refuse.New("%s: %v", ev.Name, err)
		}
		out[f.Name] = v
	}
	for name := range e.Fields {
		if _, ok := ev.Field(name); !ok {
			return refuse.New("%q is not a field of %s", name, ev.Name)
		}
	}
	e.Fields = out
	return nil
}

// Coerce reads v as a value of f's kind. JSON numbers arrive as float64, and a Go
// caller's ints are taken as numbers too.
func (f Field) Coerce(v any) (any, error) {
	switch f.Kind {
	case Text:
		if s, ok := v.(string); ok {
			return s, nil
		}
	case Choice:
		if s, ok := v.(string); ok {
			for _, x := range f.Values {
				if x == s {
					return s, nil
				}
			}
			return nil, fmt.Errorf("%q is not one of %s", s, strings.Join(f.Values, ", "))
		}
	case Flag:
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case Whole, Real:
		if n, ok := number64(v); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
			if f.Kind == Whole && n != math.Trunc(n) {
				return nil, fmt.Errorf("%v is not a whole number", n)
			}
			return n, nil
		}
	case Texts:
		switch l := v.(type) {
		case []string:
			return l, nil
		case []any:
			out := make([]string, 0, len(l))
			for _, x := range l {
				s, ok := x.(string)
				if !ok {
					return nil, fmt.Errorf("%s must be a list of strings", f.Name)
				}
				out = append(out, s)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("%s must be a %s", f.Name, f.Kind)
}

func number64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
