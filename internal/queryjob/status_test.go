package queryjob

import "testing"

func TestStatus_CanTransition(t *testing.T) {
	cases := []struct {
		from Status
		to   Status
		want bool
	}{
		{StatusPending, StatusQueued, true},
		{StatusPending, StatusFailed, true},
		{StatusQueued, StatusGenerating, true},
		{StatusQueued, StatusFailed, true},
		{StatusGenerating, StatusExecuting, true},
		{StatusGenerating, StatusFailed, true},
		{StatusExecuting, StatusSucceeded, true},
		{StatusExecuting, StatusFailed, true},

		// Illegal / skipping transitions.
		{StatusPending, StatusGenerating, false},
		{StatusPending, StatusExecuting, false},
		{StatusPending, StatusSucceeded, false},
		{StatusQueued, StatusExecuting, false},
		{StatusQueued, StatusSucceeded, false},
		{StatusGenerating, StatusSucceeded, false},
		{StatusGenerating, StatusQueued, false},

		// Terminal states can never move back to any processing state.
		{StatusSucceeded, StatusQueued, false},
		{StatusSucceeded, StatusGenerating, false},
		{StatusSucceeded, StatusExecuting, false},
		{StatusSucceeded, StatusFailed, false},
		{StatusSucceeded, StatusPending, false},
		{StatusFailed, StatusPending, false},
		{StatusFailed, StatusQueued, false},
		{StatusFailed, StatusGenerating, false},
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
	nonTerminal := []Status{StatusPending, StatusQueued, StatusGenerating, StatusExecuting}

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
