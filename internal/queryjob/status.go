package queryjob

// Status is the lifecycle state of a query job.
//
// Logical flow (Stage 3: async via RabbitMQ):
//
//	pending -> queued -> generating -> executing -> succeeded
//	pending / queued / generating / executing -> failed
//
// Each state is persisted: "pending" on create, "queued" after the API publishes
// the message, "generating" / "executing" as the worker progresses, and the
// terminal state on completion.
type Status string

const (
	StatusPending    Status = "pending"
	StatusQueued     Status = "queued"
	StatusGenerating Status = "generating"
	StatusExecuting  Status = "executing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// validTransitions maps each status to the set of statuses it may move to.
var validTransitions = map[Status][]Status{
	StatusPending:    {StatusQueued, StatusFailed},
	StatusQueued:     {StatusGenerating, StatusFailed},
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
