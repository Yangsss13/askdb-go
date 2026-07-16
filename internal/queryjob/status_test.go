package queryjob

import "testing"

func TestStatus_CanTransition(t *testing.T) {
	cases := []struct {
		from Status
		to   Status
		want bool
	}{
		{StatusPending, StatusGenerating, true},
		{StatusPending, StatusFailed, true},
		{StatusGenerating, StatusExecuting, true},
		{StatusGenerating, StatusFailed, true},
		{StatusExecuting, StatusSucceeded, true},
		{StatusExecuting, StatusFailed, true},

		// Illegal / skipping transitions.
		{StatusPending, StatusExecuting, false},
		{StatusPending, StatusSucceeded, false},
		{StatusGenerating, StatusSucceeded, false},

		// Terminal states can never move back to processing.
		{StatusSucceeded, StatusExecuting, false},
		{StatusSucceeded, StatusFailed, false},
		{StatusSucceeded, StatusPending, false},
		{StatusFailed, StatusPending, false},
		{StatusFailed, StatusExecuting, false},
		{StatusFailed, StatusSucceeded, false},
	}

	for _, c := range cases {
		if got := c.from.CanTransition(c.to); got != c.want {
			t.Errorf("CanTransition(%s -> %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestStatus_IsTerminal(t *testing.T) {
	terminal := []Status{StatusSucceeded, StatusFailed}
	nonTerminal := []Status{StatusPending, StatusGenerating, StatusExecuting}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("expected %s to be non-terminal", s)
		}
	}
}
