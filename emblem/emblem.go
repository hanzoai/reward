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
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hanzoai/reward/refuse"
)

// Card is what the picture shows.
type Card struct {
	Handle string
	// Level is the level's name.
	Level  string
	Points int64
	// Badges are the names of the subject's top badges; only the first three are drawn.
	Badges []string
}

// Options are the picture's look.
type Options struct {
	// Theme is dark or light; empty is dark.
	Theme string
	// Style is flat, a one-line label, or card; empty is flat.
	Style string
}

// Parse reads the options from a query's values, refusing any it does not know.
func Parse(theme, style string) (Options, error) {
	o := Options{Theme: theme, Style: style}
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
	return o, nil
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
		return card(c, p)
	}
	return flat(c, p)
}

func points(n int64) string { return strconv.FormatInt(n, 10) + " pts" }

func flat(c Card, p palette) []byte {
	label := "hanzo rewards"
	value := clip(c.Level, 24) + " · " + points(c.Points)
	lw, vw := width(label, 11)+14, width(value, 11)+14
	w := lw + vw
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s">`, w, esc(label+": "+value))
	fmt.Fprintf(&b, `<title>%s</title>`, esc(label+": "+value))
	fmt.Fprintf(&b, `<rect width="%d" height="20" rx="3" fill="%s"/>`, w, p.bg)
	fmt.Fprintf(&b, `<rect x="%d" width="%d" height="20" rx="3" fill="%s"/>`, lw, vw, p.accent)
	fmt.Fprintf(&b, `<rect x="%d" width="6" height="20" fill="%s"/>`, lw, p.accent)
	fmt.Fprintf(&b, `<rect width="%d" height="20" rx="3" fill="none" stroke="%s"/>`, w, p.edge)
	fmt.Fprintf(&b, `<g %s font-size="11" text-anchor="middle">`, font)
	fmt.Fprintf(&b, `<text x="%d" y="14" fill="%s">%s</text>`, lw/2, p.fg, esc(label))
	fmt.Fprintf(&b, `<text x="%d" y="14" fill="#ffffff">%s</text>`, lw+vw/2, esc(value))
	b.WriteString(`</g></svg>`)
	return []byte(b.String())
}

func card(c Card, p palette) []byte {
	const w, h = 340, 120
	c.Handle, c.Level = clip(c.Handle, 24), clip(c.Level, 24)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" role="img" aria-label="%s">`, w, h, esc(c.Handle+": "+c.Level+", "+points(c.Points)))
	fmt.Fprintf(&b, `<title>%s</title>`, esc(c.Handle+": "+c.Level+", "+points(c.Points)))
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="10" fill="%s" stroke="%s"/>`, w-1, h-1, p.bg, p.edge)
	fmt.Fprintf(&b, `<rect x="16" y="16" width="4" height="34" rx="2" fill="%s"/>`, p.accent)
	fmt.Fprintf(&b, `<g %s>`, font)
	fmt.Fprintf(&b, `<text x="30" y="30" font-size="14" font-weight="bold" fill="%s">%s</text>`, p.fg, esc(c.Handle))
	fmt.Fprintf(&b, `<text x="30" y="46" font-size="11" fill="%s">%s · %s</text>`, p.mute, esc(c.Level), esc(points(c.Points)))
	fmt.Fprintf(&b, `<text x="%d" y="30" font-size="10" text-anchor="end" fill="%s">hanzo rewards</text>`, w-16, p.mute)
	x := 16
	for i, name := range c.Badges {
		if i == 3 {
			break
		}
		name = clip(name, 18)
		pw := width(name, 10) + 16
		if x+pw > w-16 {
			break
		}
		fmt.Fprintf(&b, `<rect x="%d" y="72" width="%d" height="22" rx="11" fill="%s" stroke="%s"/>`, x, pw, p.pill, p.edge)
		fmt.Fprintf(&b, `<circle cx="%d" cy="83" r="3" fill="%s"/>`, x+10, p.accent)
		fmt.Fprintf(&b, `<text x="%d" y="86.5" font-size="10" fill="%s">%s</text>`, x+18, p.fg, esc(name))
		x += pw + 6
	}
	if len(c.Badges) == 0 {
		fmt.Fprintf(&b, `<text x="16" y="87" font-size="10" fill="%s">no badges yet</text>`, p.mute)
	}
	b.WriteString(`</g></svg>`)
	return []byte(b.String())
}

// ETag is a strong validator over the drawn bytes.
func ETag(svg []byte) string {
	sum := sha256.Sum256(svg)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}
