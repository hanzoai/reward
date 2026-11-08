package rule

import (
	"fmt"
	"strings"

	"github.com/hanzoai/reward/catalog"
)

// Matches reports whether every condition of r holds for e, and if not, which one
// did not. e must have been validated.
func (r *Rule) Matches(e catalog.Event) (bool, string) {
	if r.When.Event != e.Type {
		return false, fmt.Sprintf("event is %s, the rule reads %s", e.Type, r.When.Event)
	}
	ev, _ := catalog.Type(e.Type)
	for _, c := range r.If {
		f, ok := ev.Field(c.Field)
		if !ok || !c.holds(f, e.Fields[c.Field]) {
			return false, fmt.Sprintf("if %s %s %v", c.Field, c.Op, c.Value)
		}
	}
	return true, ""
}

func (c Condition) holds(f catalog.Field, have any) bool {
	if f.Kind == catalog.Texts && c.Op != Contains {
		return false
	}
	switch c.Op {
	case In:
		list, _ := c.Value.([]any)
		for _, x := range list {
			if want, err := f.Coerce(x); err == nil && have == want {
				return true
			}
		}
		return false
	case Contains:
		want, ok := c.Value.(string)
		if !ok {
			return false
		}
		if f.Kind == catalog.Texts {
			list, _ := have.([]string)
			for _, s := range list {
				if strings.EqualFold(s, want) {
					return true
				}
			}
			return false
		}
		s, _ := have.(string)
		return strings.Contains(strings.ToLower(s), strings.ToLower(want))
	}
	want, err := f.Coerce(c.Value)
	if err != nil {
		return false
	}
	switch c.Op {
	case Eq:
		return have == want
	case Ne:
		return have != want
	case Gte:
		h, w := have.(float64), want.(float64)
		return h >= w
	case Lte:
		h, w := have.(float64), want.(float64)
		return h <= w
	}
	return false
}
