// Tests for the Incident domain — currently the status state machine.
//
// Run all tests in the project:
//
//	go test ./...
//
// Run only the state-machine test:
//
//	go test -run TestCanTransition -v ./internal/domain/incident
package incident

import (
	"errors"
	"testing"
)

// TestCanTransition is the truth table for the status state machine.
// Read it as documentation: every row that asserts wantErr=nil is a
// transition the product allows; every row with ErrInvalidTransition is
// a transition the product refuses. Adding a new state means adding
// rows here AND updating allowedTransitions in incident.go — keeping
// both in sync is exactly what this test catches.
func TestCanTransition(t *testing.T) {
	cases := []struct {
		name    string
		from    Status
		to      Status
		wantErr error
	}{
		// Identity is always valid (idempotent updates).
		{"new->new", StatusNew, StatusNew, nil},
		{"closed->closed", StatusClosed, StatusClosed, nil},

		// Forward path.
		{"new->triaged", StatusNew, StatusTriaged, nil},
		{"triaged->in_progress", StatusTriaged, StatusInProgress, nil},
		{"in_progress->contained", StatusInProgress, StatusContained, nil},
		{"in_progress->resolved", StatusInProgress, StatusResolved, nil},
		{"contained->resolved", StatusContained, StatusResolved, nil},
		{"resolved->closed", StatusResolved, StatusClosed, nil},

		// Reopen.
		{"resolved->in_progress", StatusResolved, StatusInProgress, nil},
		{"closed->in_progress", StatusClosed, StatusInProgress, nil},

		// Forbidden jumps.
		{"new->resolved", StatusNew, StatusResolved, ErrInvalidTransition},
		{"new->closed", StatusNew, StatusClosed, ErrInvalidTransition},
		{"triaged->closed", StatusTriaged, StatusClosed, ErrInvalidTransition},
		{"in_progress->new", StatusInProgress, StatusNew, ErrInvalidTransition},
		{"closed->new", StatusClosed, StatusNew, ErrInvalidTransition},
		{"contained->in_progress", StatusContained, StatusInProgress, ErrInvalidTransition},

		// Unknown source state — sentinel for "garbage in DB".
		{"unknown->new", Status("garbage"), StatusNew, ErrInvalidTransition},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CanTransition(tc.from, tc.to)
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestSeverity_IsValid pins the accepted severity set. Adding a new
// severity is a schema change; this test forces a migration discussion.
func TestSeverity_IsValid(t *testing.T) {
	valid := []Severity{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}
	for _, s := range valid {
		if !s.IsValid() {
			t.Fatalf("expected %q to be valid", s)
		}
	}
	if Severity("nuclear").IsValid() {
		t.Fatalf("expected nuclear to be invalid")
	}
}

// TestStatus_IsValid pins the accepted status set.
func TestStatus_IsValid(t *testing.T) {
	valid := []Status{
		StatusNew, StatusTriaged, StatusInProgress,
		StatusContained, StatusResolved, StatusClosed,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Fatalf("expected %q to be valid", s)
		}
	}
	if Status("zombie").IsValid() {
		t.Fatalf("expected zombie to be invalid")
	}
}
