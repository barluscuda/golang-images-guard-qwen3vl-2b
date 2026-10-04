package domain

import "testing"

func TestAssessmentInvariants(t *testing.T) {
	p, err := NewPolicy("RULE SEXUAL_CONTENT | Sexual Content\nProhibit explicit content.")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		assessment Assessment
		valid      bool
	}{
		{"safe", Assessment{Reason: "No violation."}, true},
		{"violation", Assessment{Violation: true, Severity: 8, Rule: &Rule{"SEXUAL_CONTENT", "Sexual Content"}, Reason: "Explicit content."}, true},
		{"safe score", Assessment{Severity: 1, Reason: "Safe."}, false},
		{"safe rule", Assessment{Rule: &Rule{"SEXUAL_CONTENT", "Sexual Content"}, Reason: "Safe."}, false},
		{"missing rule", Assessment{Violation: true, Severity: 8, Reason: "Violation."}, false},
		{"zero violation", Assessment{Violation: true, Rule: &Rule{"SEXUAL_CONTENT", "Sexual Content"}, Reason: "Violation."}, false},
		{"unknown rule", Assessment{Violation: true, Severity: 8, Rule: &Rule{"OTHER", "Other"}, Reason: "Violation."}, false},
		{"wrong name", Assessment{Violation: true, Severity: 8, Rule: &Rule{"SEXUAL_CONTENT", "Other"}, Reason: "Violation."}, false},
		{"too severe", Assessment{Violation: true, Severity: 11, Rule: &Rule{"SEXUAL_CONTENT", "Sexual Content"}, Reason: "Violation."}, false},
		{"empty reason", Assessment{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidateAssessment(tc.assessment, p) == nil; got != tc.valid {
				t.Fatalf("valid=%v, want %v", got, tc.valid)
			}
		})
	}
}

func TestPolicyDeclarations(t *testing.T) {
	for _, text := range []string{"", "No rules", "RULE bad | Bad", "RULE A | A\nRULE A | B", "RULE A | "} {
		if _, err := NewPolicy(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	p, err := NewPolicy("RULE B | Second\nSome text\nRULE A | First")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rules()) != 2 || p.Rules()[0].ID != "A" {
		t.Fatal("rules are not canonical")
	}
	if p.Hash() == "" || p.Text() == "" {
		t.Fatal("policy snapshot missing")
	}
}
