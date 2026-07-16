package domain

import (
	"encoding/json"
	"testing"
)

func TestAllowsApprover(t *testing.T) {
	cases := []struct {
		name      string
		approvers string
		who       string
		want      bool
	}{
		{"listed", `["alice","bob"]`, "alice", true},
		{"not listed", `["alice","bob"]`, "carol", false},
		// A restricted approval must not be decidable by an anonymous caller.
		{"empty decided_by against a list", `["alice"]`, "", false},
		{"empty list is unrestricted", `[]`, "carol", true},
		{"null is unrestricted", `null`, "carol", true},
		{"absent is unrestricted", ``, "carol", true},
		// Kernel-written field: a corrupt row must not lock out every approver.
		{"unparseable is unrestricted", `{"oops":1}`, "carol", true},
		{"case sensitive", `["Alice"]`, "alice", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Approval{Approvers: json.RawMessage(tc.approvers)}
			if got := a.AllowsApprover(tc.who); got != tc.want {
				t.Fatalf("AllowsApprover(%q) with %s: got %v, want %v", tc.who, tc.approvers, got, tc.want)
			}
		})
	}
}
