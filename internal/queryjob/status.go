package queryjob

// Status is the lifecycle state of a query job.
//
// Logical flow (Stage 5: async via RabbitMQ with SQL Guard):
//
//	pending -> queued -> generating -> validating -> executing -> succeeded
//	pending / queued / generating / validating / executing -> failed
//
// Each state is persisted: "pending" on create, "queued" after the API publishes
// the message, "generating" while the LLM produces SQL, "validating" while the
// SQL Guard checks and normalizes it, "executing" while the query runs, and the
// terminal state on completion.
type Status string

const (
	StatusPending    Status = "pending"
	StatusQueued     Status = "queued"
	StatusGenerating Status = "generating"
	StatusValidating Status = "validating"
	StatusExecuting  Status = "executing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// validTransitions maps each status to the set of statuses it may move to.
var validTransitions = map[Status][]Status{
	StatusPending:    {StatusQueued, StatusFailed},
	StatusQueued:     {StatusGenerating, StatusFailed},
	StatusGenerating: {StatusValidating, StatusFailed},
	StatusValidating: {StatusExecuting, StatusFailed},
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
