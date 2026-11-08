package emblem

import (
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/reward/refuse"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// The cards are the people the pictures draw. The hostile one carries text a hostile
// admin or member could type; its goldens show it escaped.
var cards = map[string]Card{
	"plain":   {Handle: "alice", Level: "Builder", Points: 121, Tier: "Million Token Club", Badges: []string{"Stargazer", "Bug Slayer", "First Merge", "A Fourth One"}},
	"nothing": {Handle: "bob", Level: "Hacker", Points: 0},
	"one":     {Handle: "carol", Level: "Shipper", Points: 640, Badges: []string{"First Friend"}},
	"long":    {Handle: strings.Repeat("h", 30), Level: strings.Repeat("l", 30), Points: 1, Tier: strings.Repeat("t", 30), Badges: []string{strings.Repeat("b", 30), strings.Repeat("c", 30), strings.Repeat("d", 30)}},
	"hostile": {Handle: `<img src=x onerror=alert(1)>`, Level: `Lvl"<script>&`, Points: 9_999_999, Tier: `</text><script>`, Badges: []string{`</text><script>alert(1)</script>`, `a & b`, `'q'`}},
}

// A look is a card and the query that styles it, so each golden also proves the
// grammar of the query.
type look struct{ card, query string }

var looks = map[string]look{
	"default":           {"plain", ""},
	"default-nothing":   {"nothing", ""},
	"level-only":        {"plain", "fields=level"},
	"points-only":       {"plain", "fields=points"},
	"tokens-tier":       {"plain", "fields=tokens"},
	"tokens-none":       {"nothing", "fields=level,tokens"},
	"tokens-only-none":  {"nothing", "fields=tokens"},
	"badges-0":          {"nothing", "fields=level,badges&style=card"},
	"badges-1":          {"one", "fields=level,badges&style=card"},
	"badges-4":          {"plain", "fields=level,badges&style=card"},
	"badges-4-flat":     {"plain", "fields=level,badges"},
	"all-in-order":      {"plain", "fields=level,points,tokens,badges"},
	"all-reversed":      {"plain", "fields=badges,tokens,points,level"},
	"all-reversed-card": {"plain", "fields=badges,tokens,points,level&style=card"},
	"card-default":      {"plain", "style=card"},
	"dark-card":         {"plain", "theme=dark&style=card&fields=level,points,tokens,badges"},
	"light-flat":        {"plain", "theme=light&style=flat&fields=level,points,tokens"},
	"light-card":        {"plain", "theme=light&style=card&fields=level,points,tokens,badges"},
	"label-markup":      {"plain", "label=" + url.QueryEscape(`<b>&"x`)},
	"label-markup-card": {"plain", "style=card&label=" + url.QueryEscape(`<b>&"x`)},
	"label-24":          {"plain", "label=" + url.QueryEscape(strings.Repeat("é", 24))},
	"long-flat":         {"long", "fields=level,points,tokens,badges&label=" + strings.Repeat("W", 24)},
	"long-card":         {"long", "style=card&fields=level,points,tokens,badges&label=" + strings.Repeat("W", 24)},
	"label-24-card":     {"plain", "style=card&label=" + url.QueryEscape(strings.Repeat("W", 24))},
	"hostile-flat":      {"hostile", "fields=level,points,tokens,badges"},
	"hostile-card":      {"hostile", "style=card&fields=level,points,tokens,badges"},
	"hostile-light":     {"hostile", "theme=light&style=card&fields=level,points,tokens,badges"},
}

func render(t *testing.T, l look) []byte {
	t.Helper()
	q, err := url.ParseQuery(l.query)
	if err != nil {
		t.Fatal(err)
	}
	o, err := Parse(q)
	if err != nil {
		t.Fatalf("%s: %v", l.query, err)
	}
	return Render(cards[l.card], o)
}

func TestGoldenFiles(t *testing.T) {
	for name, l := range looks {
		file := filepath.Join("testdata", name+".svg")
		got := render(t, l)
		if *update {
			if err := os.WriteFile(file, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%v (run go test ./emblem -update)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from its golden file:\n got: %s\nwant: %s", file, got, want)
		}
	}
	// Every golden is a look: a file nothing renders is stale.
	files, err := filepath.Glob(filepath.Join("testdata", "*.svg"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, ok := looks[strings.TrimSuffix(filepath.Base(f), ".svg")]; !ok {
			t.Errorf("%s is not rendered by any look", f)
		}
	}
}

func TestEveryStringIsEscaped(t *testing.T) {
	for _, style := range []string{"flat", "card"} {
		q := url.Values{"style": {style}, "fields": {"level,points,tokens,badges"}, "label": {`<script>&"`}}
		o, err := Parse(q)
		if err != nil {
			t.Fatal(err)
		}
		svg := string(Render(cards["hostile"], o))
		for _, raw := range []string{"<script", "<img", "onerror=alert(1)>", "</text><script"} {
			if strings.Contains(svg, raw) {
				t.Errorf("%s: %q is in the output unescaped", style, raw)
			}
		}
		if !strings.Contains(svg, "&lt;script&gt;") || !strings.Contains(svg, "&amp;") || !strings.Contains(svg, "&#34;") {
			t.Errorf("%s: the escaped text is missing: %s", style, svg)
		}
	}
}

func TestOnlyThreeBadgesAreDrawn(t *testing.T) {
	for _, style := range []string{"flat", "card"} {
		svg := string(render(t, look{"plain", "style=" + style + "&fields=badges"}))
		if strings.Contains(svg, "A Fourth One") || !strings.Contains(svg, "First Merge") {
			t.Errorf("%s: badges drawn: %s", style, svg)
		}
	}
}

func TestAFieldWithNothingToShowIsLeftOut(t *testing.T) {
	for _, style := range []string{"flat", "card"} {
		svg := string(render(t, look{"nothing", "style=" + style + "&fields=tokens,badges,level"}))
		if !strings.Contains(svg, "Hacker") || strings.Contains(svg, "no badges") || strings.Contains(svg, "<circle") {
			t.Errorf("%s: %s", style, svg)
		}
	}
	// Nothing at all to show leaves the label.
	svg := string(render(t, look{"nothing", "fields=tokens"}))
	if !strings.Contains(svg, ">Hanzo</text>") || strings.Count(svg, "<text") != 1 {
		t.Errorf("only the label is drawn: %s", svg)
	}
}

func TestTheFieldsAreDrawnInTheOrderGiven(t *testing.T) {
	svg := string(render(t, look{"plain", "fields=tokens,points,level"}))
	a, b, c := strings.Index(svg, "Million Token Club"), strings.Index(svg, "121 pts"), strings.Index(svg, "Builder")
	if !(0 < a && a < b && b < c) {
		t.Errorf("order: %d %d %d in %s", a, b, c, svg)
	}
}

func TestTheSameStandingIsTheSameBytesAndTag(t *testing.T) {
	a := render(t, looks["default"])
	b := render(t, looks["default"])
	other := render(t, looks["default-nothing"])
	if string(a) != string(b) || ETag(a) != ETag(b) || ETag(a) == ETag(other) {
		t.Error("render is not a pure function of the card")
	}
	if tag := ETag(a); !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
		t.Errorf("tag = %s", tag)
	}
}

func TestEveryOptionChangesTheTag(t *testing.T) {
	seen := map[string]string{}
	for _, q := range []string{"", "theme=light", "style=card", "fields=level", "fields=points,level", "fields=level,points,tokens", "fields=level,badges&style=card", "label=Zoo"} {
		tag := ETag(render(t, look{"plain", q}))
		if prev, dup := seen[tag]; dup {
			t.Errorf("%q and %q have the same tag %s", prev, q, tag)
		}
		seen[tag] = q
	}
}

func TestOptionsDefaultAndRefuse(t *testing.T) {
	o, err := Parse(url.Values{})
	if err != nil || o.Theme != "dark" || o.Style != "flat" || o.Label != "Hanzo" || strings.Join(o.Fields, ",") != "level,points" {
		t.Errorf("defaults = %+v %v", o, err)
	}
	// An empty theme or style is the default, as it always was.
	if o, err := Parse(url.Values{"theme": {""}, "style": {""}}); err != nil || o.Theme != "dark" || o.Style != "flat" {
		t.Errorf("empty theme and style = %+v %v", o, err)
	}
	ok, err := Parse(url.Values{"fields": {"badges,level,tokens,points"}, "label": {"x"}, "theme": {"light"}, "style": {"card"}})
	if err != nil || strings.Join(ok.Fields, ",") != "badges,level,tokens,points" || ok.Label != "x" || ok.Theme != "light" || ok.Style != "card" {
		t.Errorf("options = %+v %v", ok, err)
	}
	// Defaults are the caller's own: changing one picture's fields changes no other's.
	o.Fields[0] = "badges"
	if again, _ := Parse(url.Values{}); again.Fields[0] != "level" {
		t.Error("the default fields are shared")
	}

	for name, q := range map[string]url.Values{
		"theme":           {"theme": {"neon"}},
		"style":           {"style": {"huge"}},
		"theme case":      {"theme": {"DARK"}},
		"unknown field":   {"fields": {"level,rank"}},
		"duplicate field": {"fields": {"level,points,level"}},
		"empty fields":    {"fields": {""}},
		"empty name":      {"fields": {"level,,points"}},
		"trailing comma":  {"fields": {"level,"}},
		"space in a name": {"fields": {"level, points"}},
		"field case":      {"fields": {"Level"}},
		"empty label":     {"label": {""}},
		"label 25":        {"label": {strings.Repeat("a", 25)}},
		"label 25 runes":  {"label": {strings.Repeat("é", 25)}},
		"newline":         {"label": {"a\nb"}},
		"carriage return": {"label": {"a\rb"}},
		"tab":             {"label": {"a\tb"}},
		"nul":             {"label": {"a\x00b"}},
		"delete":          {"label": {"a\x7fb"}},
		"c1 control":      {"label": {"a\u0085b"}},
		"line separator":  {"label": {"a b"}},
		"para separator":  {"label": {"a b"}},
		"invalid utf-8":   {"label": {"a\xffb"}},
	} {
		if _, err := Parse(q); !refuse.Is(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, label := range map[string]string{"one": "x", "24": strings.Repeat("a", 24), "24 runes": strings.Repeat("é", 24), "spaces": "a b  c", "emoji": "🔥"} {
		if o, err := Parse(url.Values{"label": {label}}); err != nil || o.Label != label {
			t.Errorf("%s: %+v %v", name, o, err)
		}
	}
}
