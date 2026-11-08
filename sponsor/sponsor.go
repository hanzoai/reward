// Package sponsor is who brought a member in: one link per member, written once
// and never changed.
//
// A Link is keyed by the member (ID) and inserted by a conditional insert, so the
// first writer wins on any backend and a member's sponsor is whoever was written
// first. Nothing edits or deletes a link: upline points are paid along these links,
// and a link that moved would move points already paid.
package sponsor

import "github.com/hanzoai/orm"

// Kind is the link's kind in the store.
const Kind = "reward-sponsor"

func init() { orm.Register[Link](Kind, orm.WithStringKey[Link]()) }

// Link says that Sponsor brought Subject in.
type Link struct {
	orm.Model[Link]

	Subject string `json:"subject"`
	Sponsor string `json:"sponsor" orm:"index"`
	// Event is the id of the referral.linked event that made the link.
	Event string `json:"event"`
}

// ID is the store id of the link of a member. Every kind keeps ids in a keyspace of
// its own, because the store's id column is one key for all kinds.
func ID(subject string) string { return "s:" + subject }
