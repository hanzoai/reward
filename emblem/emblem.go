// Package emblem draws a subject's standing as an SVG, for a README to embed.
//
// The picture is a pure function of its Card and Options, so the same standing is the
// same bytes and an ETag over them is honest. Every piece of text is escaped: a
// handle, a level name or a badge name is a person's or an admin's string, and an
// SVG is a document a browser runs.
package emblem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/reward/refuse"
)

// Card is what the picture shows.
type Card struct {
	Handle string
	// Level is the level's name.
	Level  string
	Points int64
	// Tier is the name of the highest token milestone the subject has reached; empty
	// when they have reached none.
	Tier string
	// Badges are the names of the subject's top badges; only the first three are drawn.
	Badges []string
}

// Options are the picture's look.
type Options struct {
	// Theme is dark or light.
	Theme string
	// Style is flat, a one-line label, or card.
	Style string
	// Fields are what the picture says, in order. A flat label says them all on one
	// line; a card says level, points and tokens under the handle, and draws badges
	// as a row of pills.
	Fields []string
	// Label is the left label of a flat picture and the corner label of a card.
	Label string
}

// MaxLabel is the longest label, in characters.
const MaxLabel = 24

// fieldNames are the four things a picture can say.
var fieldNames = []string{"level", "points", "tokens", "badges"}

// Parse reads the options from a query, refusing any it does not know or cannot
// draw. theme and style default to dark and flat (and an empty value is the default);
// fields defaults to level,points and, once sent, must name at least one of level,
// points, tokens and badges, each once; label defaults to Hanzo and, once sent, is
// one to 24 characters of single-line text.
func Parse(q url.Values) (Options, error) {
	o := Options{Theme: q.Get("theme"), Style: q.Get("style"), Label: "Hanzo"}
	if o.Theme == "" {
		o.Theme = "dark"
	}
	if o.Style == "" {
		o.Style = "flat"
	}
	if o.Theme != "dark" && o.Theme != "light" {
		return o, refuse.New("theme must be dark or light")
	}
	if o.Style != "flat" && o.Style != "card" {
		return o, refuse.New("style must be flat or card")
	}
	if !q.Has("fields") {
		o.Fields = []string{"level", "points"}
	} else {
		for _, f := range strings.Split(q.Get("fields"), ",") {
			if !slices.Contains(fieldNames, f) {
				return o, refuse.New("fields must be a comma list of level, points, tokens and badges")
			}
			if slices.Contains(o.Fields, f) {
				return o, refuse.New("fields must name each of level, points, tokens and badges once")
			}
			o.Fields = append(o.Fields, f)
		}
	}
	if q.Has("label") {
		o.Label = q.Get("label")
		if n := utf8.RuneCountInString(o.Label); n == 0 || n > MaxLabel {
			return o, refuse.New("label must be 1 to %d characters", MaxLabel)
		}
		if !utf8.ValidString(o.Label) || strings.ContainsFunc(o.Label, isBreak) {
			return o, refuse.New("label must be one line of text")
		}
	}
	return o, nil
}

// isBreak is a rune that is not part of a single line of text: a control character,
// which includes every newline, or a Unicode line or paragraph separator.
func isBreak(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp)
}

type palette struct{ bg, fg, mute, edge, accent, pill string }

var themes = map[string]palette{
	"dark":  {bg: "#0d0d0f", fg: "#f4f4f5", mute: "#a1a1aa", edge: "#27272a", accent: "#ff5a1f", pill: "#18181b"},
	"light": {bg: "#ffffff", fg: "#18181b", mute: "#52525b", edge: "#e4e4e7", accent: "#d9420b", pill: "#f4f4f5"},
}

const font = `font-family="Verdana,Geneva,DejaVu Sans,sans-serif"`

// width estimates the pixels a text takes at the given size: a fixed advance per
// character, since the file carries no font to measure with.
func width(s string, size float64) int {
	return int(float64(utf8.RuneCountInString(s))*size*0.62) + 1
}

func esc(s string) string { return html.EscapeString(s) }

// clip shortens s to at most n characters, with an ellipsis, so a long name cannot
// run out of the picture.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Render draws c.
func Render(c Card, o Options) []byte {
	p := themes[o.Theme]
	if o.Style == "card" {
		return card(c, o, p)
	}
	return flat(c, o, p)
}

func points(n int64) string { return strconv.FormatInt(n, 10) + " pts" }

// names are the badge names that are drawn: the first three, each short enough to
// stay in the picture.
func names(c Card) []string {
	out := make([]string, 0, 3)
	for _, n := range c.Badges[:min(3, len(c.Badges))] {
		out = append(out, clip(n, 18))
	}
	return out
}

// say is what one field says about c, or "" when it has nothing to say.
func say(c Card, field string) string {
	switch field {
	case "level":
		return clip(c.Level, 24)
	case "points":
		return points(c.Points)
	case "tokens":
		return clip(c.Tier, 24)
	}
	return strings.Join(names(c), ", ")
}

// join puts the parts that say something one after another.
func join(sep string, parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), sep)
}

// line is what the fields say, in order, one after another; a field with nothing to
// say is left out.
func line(c Card, fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = say(c, f)
	}
	return join(" · ", parts...)
}

func flat(c Card, o Options, p palette) []byte {
	value := line(c, o.Fields)
	label := o.Label
	said := label
	lw, vw := width(label, 11)+14, 0
	w := lw
	if value != "" {
		said = label + ": " + value
		vw = width(value, 11) + 14
		w += vw
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s">`, w, esc(said))
	fmt.Fprintf(&b, `<title>%s</title>`, esc(said))
	fmt.Fprintf(&b, `<rect width="%d" height="20" rx="3" fill="%s"/>`, w, p.bg)
	if value != "" {
		fmt.Fprintf(&b, `<rect x="%d" width="%d" height="20" rx="3" fill="%s"/>`, lw, vw, p.accent)
		fmt.Fprintf(&b, `<rect x="%d" width="6" height="20" fill="%s"/>`, lw, p.accent)
	}
	fmt.Fprintf(&b, `<rect width="%d" height="20" rx="3" fill="none" stroke="%s"/>`, w, p.edge)
	fmt.Fprintf(&b, `<g %s font-size="11" text-anchor="middle">`, font)
	fmt.Fprintf(&b, `<text x="%d" y="14" fill="%s">%s</text>`, lw/2, p.fg, esc(label))
	if value != "" {
		fmt.Fprintf(&b, `<text x="%d" y="14" fill="#ffffff">%s</text>`, lw+vw/2, esc(value))
	}
	b.WriteString(`</g></svg>`)
	return []byte(b.String())
}

func card(c Card, o Options, p palette) []byte {
	c.Handle = clip(c.Handle, 24)
	sub := line(c, slices.DeleteFunc(slices.Clone(o.Fields), func(f string) bool { return f == "badges" }))
	var pills []string
	if slices.Contains(o.Fields, "badges") {
		pills = names(c)
	}
	// The card is 340 wide, or wider to hold its handle beside its label, or its
	// line under the handle. The handle is bold and a label may be all wide letters,
	// which width underestimates, so each is measured a little large.
	w := max(340, 30+width(c.Handle, 16)+16+width(o.Label, 15)+16, 30+width(sub, 11)+16)
	h := 66
	if len(pills) > 0 {
		h = 120
	}
	said := c.Handle
	if rest := join(" · ", sub, join(", ", pills...)); rest != "" {
		said += ": " + rest
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" role="img" aria-label="%s">`, w, h, esc(said))
	fmt.Fprintf(&b, `<title>%s</title>`, esc(said))
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="10" fill="%s" stroke="%s"/>`, w-1, h-1, p.bg, p.edge)
	fmt.Fprintf(&b, `<rect x="16" y="16" width="4" height="34" rx="2" fill="%s"/>`, p.accent)
	fmt.Fprintf(&b, `<g %s>`, font)
	fmt.Fprintf(&b, `<text x="30" y="30" font-size="14" font-weight="bold" fill="%s">%s</text>`, p.fg, esc(c.Handle))
	if sub != "" {
		fmt.Fprintf(&b, `<text x="30" y="46" font-size="11" fill="%s">%s</text>`, p.mute, esc(sub))
	}
	fmt.Fprintf(&b, `<text x="%d" y="30" font-size="10" text-anchor="end" fill="%s">%s</text>`, w-16, p.mute, esc(o.Label))
	x := 16
	for _, name := range pills {
		pw := width(name, 10) + 16
		if x+pw > w-16 {
			break
		}
		fmt.Fprintf(&b, `<rect x="%d" y="72" width="%d" height="22" rx="11" fill="%s" stroke="%s"/>`, x, pw, p.pill, p.edge)
		fmt.Fprintf(&b, `<circle cx="%d" cy="83" r="3" fill="%s"/>`, x+10, p.accent)
		fmt.Fprintf(&b, `<text x="%d" y="86.5" font-size="10" fill="%s">%s</text>`, x+18, p.fg, esc(name))
		x += pw + 6
	}
	b.WriteString(`</g></svg>`)
	return []byte(b.String())
}

// ETag is a strong validator over the drawn bytes.
func ETag(svg []byte) string {
	sum := sha256.Sum256(svg)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}
