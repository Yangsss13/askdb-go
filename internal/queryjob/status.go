package queryjob

// Status is the lifecycle state of a query job.
//
// Logical flow (Stage 2 executes synchronously within a single request):
//
//	pending -> generating -> executing -> succeeded
//	pending / generating / executing -> failed
//
// To avoid extra database round-trips in the synchronous path, only "pending"
// (on create) and the terminal state (on finish) are persisted. The intermediate
// states are still modeled here so the transition rules can be validated and
// unit-tested, and so a future asynchronous worker can persist each step.
type Status string

const (
	StatusPending    Status = "pending"
	StatusGenerating Status = "generating"
	StatusExecuting  Status = "executing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// validTransitions maps each status to the set of statuses it may move to.
var validTransitions = map[Status][]Status{
	StatusPending:    {StatusGenerating, StatusFailed},
	StatusGenerating: {StatusExecuting, StatusFailed},
	StatusExecuting:  {StatusSucceeded, StatusFailed},
	StatusSucceeded:  {},
	StatusFailed:     {},
}

// IsTerminal reports whether s is a final state that cannot transition further.
func (s Status) IsTerminal() bool {
	return s == StatusSucceeded || s == StatusFailed
}

// CanTransition reports whether moving from s to next is a legal transition.
// Terminal states (succeeded, failed) can never move to a processing state.
func (s Status) CanTransition(next Status) bool {
	for _, allowed := range validTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}
