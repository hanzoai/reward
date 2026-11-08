package emblem

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/reward/refuse"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// The goldens pin the exact bytes of each look. The card carries text a hostile
// admin or member could type; the goldens show it escaped.
var cards = map[string]Card{
	"plain":   {Handle: "alice", Level: "Builder", Points: 121, Badges: []string{"Stargazer", "Bug Slayer", "First Merge", "A Fourth One"}},
	"nobadge": {Handle: "bob", Level: "Hacker", Points: 0},
	"hostile": {Handle: `<img src=x onerror=alert(1)>`, Level: `Lvl"<script>&`, Points: 9_999_999, Badges: []string{`</text><script>alert(1)</script>`, `a & b`, `'q'`}},
}

func TestGoldenFiles(t *testing.T) {
	for name, card := range cards {
		for _, theme := range []string{"dark", "light"} {
			for _, style := range []string{"flat", "card"} {
				file := filepath.Join("testdata", name+"-"+theme+"-"+style+".svg")
				got := Render(card, Options{Theme: theme, Style: style})
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
		}
	}
}

func TestEveryStringIsEscaped(t *testing.T) {
	for _, style := range []string{"flat", "card"} {
		svg := string(Render(cards["hostile"], Options{Theme: "dark", Style: style}))
		for _, raw := range []string{"<script", "<img", "onerror=alert(1)>", "</text><script"} {
			if strings.Contains(svg, raw) {
				t.Errorf("%s: %q is in the output unescaped", style, raw)
			}
		}
		if !strings.Contains(svg, "&lt;script&gt;") || !strings.Contains(svg, "&amp;") {
			t.Errorf("%s: the escaped text is missing: %s", style, svg)
		}
	}
}

func TestOnlyThreeBadgesAreDrawn(t *testing.T) {
	svg := string(Render(cards["plain"], Options{Theme: "dark", Style: "card"}))
	if strings.Contains(svg, "A Fourth One") || !strings.Contains(svg, "First Merge") {
		t.Errorf("badges drawn: %s", svg)
	}
}

func TestTheSameStandingIsTheSameBytesAndTag(t *testing.T) {
	a := Render(cards["plain"], Options{Theme: "dark", Style: "flat"})
	b := Render(cards["plain"], Options{Theme: "dark", Style: "flat"})
	other := Render(cards["nobadge"], Options{Theme: "dark", Style: "flat"})
	if string(a) != string(b) || ETag(a) != ETag(b) || ETag(a) == ETag(other) {
		t.Error("render is not a pure function of the card")
	}
	if tag := ETag(a); !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
		t.Errorf("tag = %s", tag)
	}
}

func TestOptionsDefaultAndRefuse(t *testing.T) {
	o, err := Parse("", "")
	if err != nil || o.Theme != "dark" || o.Style != "flat" {
		t.Errorf("defaults = %+v %v", o, err)
	}
	for _, bad := range [][2]string{{"neon", ""}, {"", "huge"}, {"DARK", ""}} {
		if _, err := Parse(bad[0], bad[1]); !refuse.Is(err) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}
