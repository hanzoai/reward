// Package credit is how the engine grants credit: by asking commerce's one credit
// path, POST /v1/billing/credit. It writes no ledger of its own.
//
// Commerce gates that route on the platform principal (a SuperAdmin) and makes the
// grant exactly once per idempotencyKey. Who the call is made as is the host's
// business and not this package's: Doer is the host's transport, and in the cloud
// binary it is the in-process one that states the platform principal, so no
// credential of this module's own is held or sent.
package credit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hanzoai/reward/engine"
)

// Doer sends one request. *http.Client is one; so is a host's in-process transport.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client grants credit through commerce. It satisfies engine.Credits.
type Client struct {
	// Base is commerce's address, such as https://api.hanzo.ai. The route is
	// Base + /v1/billing/credit.
	Base string
	// Doer carries the request; nil is http.DefaultClient.
	Doer Doer
	// Max is the most one grant may be, in cents. Commerce enforces its own
	// ceiling behind this one; this is the bound a rule is checked against when it
	// is saved, so a rule that commerce would refuse is refused up front. 0 takes
	// DefaultMax.
	Max int64
}

// DefaultMax is commerce's own default ceiling on one grant (COMMERCE_DEPOSIT_MAX_CENTS).
const DefaultMax = 100_000_000

var _ engine.Credits = Client{}

// Ceiling is the most one grant may be, in cents.
func (c Client) Ceiling() int64 {
	if c.Max > 0 {
		return c.Max
	}
	return DefaultMax
}

type request struct {
	Org            string `json:"org"`
	AmountCents    int64  `json:"amountCents"`
	Reason         string `json:"reason"`
	Tag            string `json:"tag"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// Grant asks commerce to credit g.Org with g.Cents, once per g.Key. A replay of a
// grant commerce already made answers 200 and is a success.
func (c Client) Grant(ctx context.Context, g engine.Grant) error {
	body, err := json.Marshal(request{Org: g.Org, AmountCents: g.Cents, Reason: g.Reason + " (approved by " + g.Actor + ")", Tag: g.Tag, IdempotencyKey: g.Key})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Base, "/")+"/v1/billing/credit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var d Doer = http.DefaultClient
	if c.Doer != nil {
		d = c.Doer
	}
	resp, err := d.Do(req)
	if err != nil {
		return fmt.Errorf("credit: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("credit: commerce answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}
