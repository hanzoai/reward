package http

import (
	"strings"

	nethttp "net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward/emblem"
	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/profile"
)

// explore lists the active rules the caller has not earned yet, grouped by pillar in
// order (use, share, community, contribute, expert), each with its points, badges and
// the catalog event that earns it.
func (h handlers) explore(c *zip.Ctx) error {
	w, ok := member(c)
	if !ok {
		return nil
	}
	out, err := h.e.Explore(c.Context(), w.subject)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"pillars": out})
}

// myProfile reads the caller's own profile: handle, whether it is public, links.
func (h handlers) myProfile(c *zip.Ctx) error {
	w, ok := member(c)
	if !ok {
		return nil
	}
	p, err := h.e.Profile(c.Context(), w.subject)
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, body{p.Handle, p.Public, p.Links})
}

// body is what a profile PUT may set.
type body struct {
	Handle string        `json:"handle"`
	Public bool          `json:"public"`
	Links  profile.Links `json:"links"`
}

// saveMyProfile sets the caller's handle, whether their standing is public, and their
// links, which must be https URLs. A public profile needs a handle; a handle someone
// else holds is 409. The profile saved is always the caller's own.
func (h handlers) saveMyProfile(c *zip.Ctx) error {
	w, ok := member(c)
	if !ok {
		return nil
	}
	var in body
	if err := c.Bind(&in); err != nil {
		return fail(c, nethttp.StatusBadRequest, "invalid request body", err)
	}
	p, err := h.e.SaveProfile(c.Context(), w.subject, profile.Profile{Handle: in.Handle, Public: in.Public, Links: in.Links})
	if err != nil {
		return h.answer(c, err)
	}
	return c.JSON(nethttp.StatusOK, body{p.Handle, p.Public, p.Links})
}

// show reads a public profile by handle: level, lifetime points, badges, counts per
// pillar and the links the member gave. It exists only for a member who turned it on;
// any other handle is 404. It never carries an email or a redeemable balance.
func (h handlers) show(c *zip.Ctx) error {
	out, err := h.e.Show(c.Context(), c.Param("handle"), c.Query("program"))
	if err != nil {
		return h.answer(c, err)
	}
	c.SetHeader("Cache-Control", "public, max-age=60")
	return c.JSON(nethttp.StatusOK, out)
}

// emblem draws a public profile as an SVG for a README: level, points and up to three
// top badges. ?theme=dark|light and ?style=flat|card. It is refetched every time
// (no-cache) and revalidated by ETag, so GitHub's image proxy follows the standing.
func (h handlers) emblem(c *zip.Ctx) error {
	handle, ok := strings.CutSuffix(c.Param("handle"), ".svg")
	if !ok {
		return h.answer(c, engine.ErrNotFound)
	}
	o, err := emblem.Parse(c.Query("theme"), c.Query("style"))
	if err != nil {
		return h.answer(c, err)
	}
	p, err := h.e.Show(c.Context(), handle, c.Query("program"))
	if err != nil {
		return h.answer(c, err)
	}
	card := emblem.Card{Handle: p.Handle, Level: p.LevelName, Points: p.Points}
	for _, f := range p.Top(3) {
		card.Badges = append(card.Badges, f.Name)
	}
	svg := emblem.Render(card, o)
	tag := emblem.ETag(svg)
	c.SetHeader("Content-Type", "image/svg+xml; charset=utf-8")
	c.SetHeader("Cache-Control", "no-cache, max-age=0")
	c.SetHeader("ETag", tag)
	c.SetHeader("X-Content-Type-Options", "nosniff")
	c.SetHeader("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	if c.Header("If-None-Match") == tag {
		return c.Bytes(nethttp.StatusNotModified, nil)
	}
	return c.Bytes(nethttp.StatusOK, svg)
}
