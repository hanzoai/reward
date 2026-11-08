package claim

import "testing"

func steps(s ...Step) []*Step {
	out := make([]*Step, len(s))
	for i := range s {
		out[i] = &s[i]
	}
	return out
}

func TestAClaimsStatusIsTheFoldOfItsSteps(t *testing.T) {
	approve := Step{Act: Decision, Verdict: Approve}
	reject := Step{Act: Decision, Verdict: Reject}
	award := Step{Act: Award}
	reversal := Step{Act: Reversal}
	cases := []struct {
		name  string
		steps []*Step
		want  Status
	}{
		{"none", nil, Pending},
		{"approved", steps(approve), Approved},
		{"rejected", steps(reject), Rejected},
		{"awarded", steps(approve, award), Awarded},
		{"reversed", steps(approve, award, reversal), Reversed},
		{"a reversal with no award is nothing", steps(approve, reversal), Approved},
		{"order does not matter", steps(award, reversal, approve), Reversed},
	}
	for _, c := range cases {
		if got := Fold(c.steps); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestIDsAreStableAndDistinct(t *testing.T) {
	if ID("e", "r", 1) != ID("e", "r", 1) {
		t.Error("an id changed between calls")
	}
	if ID("e", "r", 1) == ID("e", "r", 2) || ID("e", "r", 1) == ID("e2", "r", 1) || ID("e", "r", 1) == ID("e", "r2", 1) {
		t.Error("two claims share an id")
	}
	if OriginOf("e", "r") == ID("e", "r", 1) {
		t.Error("origin and id are one space")
	}
	if ID("a", "bc", 1) == ID("ab", "c", 1) {
		t.Error("parts run together")
	}
	if StepID("c", Decision) == StepID("c", Award) {
		t.Error("two steps share an id")
	}
}

func TestLiveStatuses(t *testing.T) {
	for s, want := range map[Status]bool{Pending: true, Approved: true, Awarded: true, Rejected: false, Reversed: false} {
		if s.Live() != want || !s.Valid() {
			t.Errorf("%s", s)
		}
	}
	if Status("done").Valid() {
		t.Error("done is not a status")
	}
}
