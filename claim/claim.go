// Package claim is one subject's claim to what one version of one rule awards for
// one event, and the steps that decide it.
//
// A claim row never changes after it is written. What is decided about it is more
// rows: a Step is written once, under an id that is a hash of the claim and the
// step, by a conditional insert that exactly one writer wins. So two reviewers
// approving and rejecting at once cannot both succeed, and an approval that runs
// twice writes once, on any backend, with no transaction. A claim's Status is the
// fold of its steps (Fold); the Status column on the row only lets a queue be
// listed, and is healed to the fold whenever the claim is read for a decision.
package claim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/rule"
)

// Kinds in the store.
const (
	Kind     = "reward-claim"
	StepKind = "reward-step"
)

func init() {
	orm.Register[Claim](Kind, orm.WithStringKey[Claim]())
	orm.Register[Step](StepKind, orm.WithStringKey[Step]())
}

// Status is where a claim stands.
//
//	pending   made, waiting for a person (manual) or for its approval to run (auto).
//	approved  a decision to award it was made; the ledger, badge and credit are
//	          being written, each exactly once, and a retry finishes them.
//	rejected  a person refused it. Final.
//	awarded   everything it earns is written.
//	reversed  everything it earned was undone by compensating entries. Final.
//
// pending -> approved -> awarded -> reversed, and pending -> rejected.
type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Rejected Status = "rejected"
	Awarded  Status = "awarded"
	Reversed Status = "reversed"
)

// Valid reports whether s is a status.
func (s Status) Valid() bool {
	switch s {
	case Pending, Approved, Rejected, Awarded, Reversed:
		return true
	}
	return false
}

// Live reports whether a claim still counts toward limits and budget.
func (s Status) Live() bool { return s == Pending || s == Approved || s == Awarded }

// Claim is a claim. Its id is a hash of the event id, the rule id and the rule
// version, so the same event twice is one claim.
type Claim struct {
	orm.Model[Claim]

	// Origin is a hash of the event id and the rule id alone: a claim for the same
	// event under another version of the rule is the same happening.
	Origin string `json:"origin" orm:"index"`

	Program     string         `json:"program" orm:"index"`
	Rule        string         `json:"rule" orm:"index"`
	RuleVersion int            `json:"ruleVersion"`
	Pillar      catalog.Pillar `json:"pillar"`
	Approval    rule.Approval  `json:"approval"`

	Event   string    `json:"event"`
	EventID string    `json:"eventId"`
	EventAt time.Time `json:"eventAt,omitzero"`
	Subject string    `json:"subject" orm:"index"`
	Org     string    `json:"org,omitempty"`
	Email   string    `json:"email,omitempty"`

	// Slot is the value of the rule's key field, when its limit reads one; Day and
	// Month are the UTC day and month the claim was made in.
	Slot  string `json:"slot,omitempty"`
	Day   string `json:"day"`
	Month string `json:"month"`

	// Points and CreditCents are what the claim's actions earn, reserved against
	// the program's budget while the claim is live.
	Points      int64 `json:"points"`
	CreditCents int64 `json:"creditCents"`

	// Status is the fold of the claim's steps as last settled: a column for
	// listing, not the truth. See the package comment.
	Status Status `json:"status" orm:"index"`

	// Fields and Then are the event's fields and the rule's actions as they were
	// when the claim was made. The claim awards its Then even if the rule has
	// changed since.
	Fields map[string]any `json:"fields"`
	Then   []rule.Action  `json:"then"`

	// Evidence is what the host stated of where the happening took place and what
	// its checks found, for the staff member who decides the claim.
	Evidence *catalog.Evidence `json:"evidence,omitempty"`

	// Reviewer, ReviewedAt and Note are the latest decision on the claim, copied
	// from its steps when it is settled.
	Reviewer   string    `json:"reviewer,omitempty"`
	ReviewedAt time.Time `json:"reviewedAt,omitzero"`
	Note       string    `json:"note,omitempty"`
}

// ID is the id of the claim for an event under one version of a rule.
func ID(eventID, ruleID string, version int) string {
	return "clm_" + Digest("claim", eventID, ruleID, fmt.Sprint(version))
}

// OriginOf is the claim's origin: the happening, whatever the rule's version.
func OriginOf(eventID, ruleID string) string { return Digest("origin", eventID, ruleID) }

// Digest is a stable hash of its parts, for ids that must be the same however many
// times and wherever they are made.
func Digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// What a Step is.
const (
	// Decision is a reviewer's verdict on a pending claim.
	Decision = "decision"
	// Award marks everything an approved claim earns as written.
	Award = "award"
	// Reversal is a reviewer undoing an awarded claim.
	Reversal = "reversal"
)

// Verdicts of a Decision.
const (
	Approve = "approve"
	Reject  = "reject"
)

// Step is one decided fact about a claim, written once. A claim has at most one of
// each: its id is a hash of the claim and the step.
type Step struct {
	orm.Model[Step]

	Claim string `json:"claim" orm:"index"`
	// Act is Decision, Award or Reversal.
	Act string `json:"act"`
	// Verdict is Approve or Reject, for a Decision.
	Verdict  string    `json:"verdict,omitempty"`
	Reviewer string    `json:"reviewer"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
}

// StepID is the id of the step act of a claim.
func StepID(claim, act string) string { return "stp_" + Digest("step", claim, act) }

// Fold is the status a claim's steps say it has.
func Fold(steps []*Step) Status {
	var decision, award, reversal *Step
	for _, s := range steps {
		switch s.Act {
		case Decision:
			decision = s
		case Award:
			award = s
		case Reversal:
			reversal = s
		}
	}
	switch {
	case reversal != nil && award != nil:
		return Reversed
	case award != nil:
		return Awarded
	case decision != nil && decision.Verdict == Approve:
		return Approved
	case decision != nil && decision.Verdict == Reject:
		return Rejected
	}
	return Pending
}

// Latest is the reviewer, time and note of the most recent step.
func Latest(steps []*Step) (reviewer string, at time.Time, note string) {
	for _, s := range steps {
		if s.At.After(at) || (s.At.Equal(at) && s.Act == Reversal) {
			reviewer, at, note = s.Reviewer, s.At, s.Note
		}
	}
	return reviewer, at, note
}
