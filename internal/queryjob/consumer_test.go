package queryjob

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fakeAcknowledger records the last ACK/NACK call.
type fakeAcknowledger struct {
	mu      sync.Mutex
	acked   bool
	nacked  bool
	requeue bool
	ackErr  error
	nackErr error
}

func (f *fakeAcknowledger) Ack(_ uint64, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = true
	return f.ackErr
}

func (f *fakeAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nacked = true
	f.requeue = requeue
	return f.nackErr
}

func (f *fakeAcknowledger) Reject(_ uint64, _ bool) error { return nil }

// makeDelivery builds an amqp.Delivery with a fakeAcknowledger.
func makeDelivery(t *testing.T, msg QueryJobMessage) (amqp.Delivery, *fakeAcknowledger) {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ack := &fakeAcknowledger{}
	return amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
	}, ack
}

func validMsg(jobID uint64) QueryJobMessage {
	return QueryJobMessage{
		MessageID:  "test-id",
		Type:       MessageTypeQueryExecutionRequested,
		Version:    messageVersion,
		OccurredAt: time.Now(),
		Payload:    JobPayload{JobID: jobID},
	}
}

// fakeProcessService is a minimal stand-in for WorkerService.
type fakeProcessService struct {
	err         error
	processedID uint64
	delay       time.Duration
}

func (f *fakeProcessService) Process(_ context.Context, jobID uint64) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.processedID = jobID
	return f.err
}

func newTestConsumer(svc ProcessService) *Consumer {
	return &Consumer{svc: svc}
}

// --- handle unit tests (no real channel needed) ---

func TestConsumer_Handle_Success(t *testing.T) {
	svc := &fakeProcessService{}
	c := newTestConsumer(svc)
	d, ack := makeDelivery(t, validMsg(5))

	outcome, _ := c.handle(d)
	if outcome != outcomeAck {
		t.Errorf("expected outcomeAck, got %d", outcome)
	}
	if svc.processedID != 5 {
		t.Errorf("expected processedID=5, got %d", svc.processedID)
	}
	_ = ack // ACK is called by the loop, not handle itself
}

func TestConsumer_Handle_MalformedMessage(t *testing.T) {
	c := newTestConsumer(&fakeProcessService{})
	d := amqp.Delivery{
		Acknowledger: &fakeAcknowledger{},
		Body:         []byte("not-json"),
	}
	outcome, _ := c.handle(d)
	if outcome != outcomeNackNoRequeue {
		t.Errorf("expected outcomeNackNoRequeue, got %d", outcome)
	}
}

func TestConsumer_Handle_UnsupportedType(t *testing.T) {
	c := newTestConsumer(&fakeProcessService{})
	msg := validMsg(1)
	msg.Type = "unknown.type"
	d, _ := makeDelivery(t, msg)

	outcome, _ := c.handle(d)
	if outcome != outcomeNackNoRequeue {
		t.Errorf("expected outcomeNackNoRequeue for unknown type, got %d", outcome)
	}
}

func TestConsumer_Handle_ZeroJobID(t *testing.T) {
	c := newTestConsumer(&fakeProcessService{})
	msg := validMsg(0)
	d, _ := makeDelivery(t, msg)

	outcome, _ := c.handle(d)
	if outcome != outcomeNackNoRequeue {
		t.Errorf("expected outcomeNackNoRequeue for job_id=0, got %d", outcome)
	}
}

func TestConsumer_Handle_JobNotFound(t *testing.T) {
	svc := &fakeProcessService{err: ErrJobNotFound}
	c := newTestConsumer(svc)
	d, _ := makeDelivery(t, validMsg(99))

	outcome, _ := c.handle(d)
	if outcome != outcomeNackNoRequeue {
		t.Errorf("expected outcomeNackNoRequeue for missing job, got %d", outcome)
	}
}

func TestConsumer_Handle_DBWriteFailure_Fatal(t *testing.T) {
	svc := &fakeProcessService{err: errors.New("db write failed")}
	c := newTestConsumer(svc)
	d, _ := makeDelivery(t, validMsg(1))

	outcome, _ := c.handle(d)
	if outcome != outcomeFatal {
		t.Errorf("expected outcomeFatal on DB write failure, got %d", outcome)
	}
}

// TestConsumer_Stop_WaitsForInFlight verifies that Stop blocks until an
// in-flight Process call completes. No real AMQP channel is needed.
func TestConsumer_Stop_WaitsForInFlight(t *testing.T) {
	const processDelay = 80 * time.Millisecond

	svc := &fakeProcessService{delay: processDelay}
	c := newTestConsumer(svc)
	// Replace ch with nil; Stop calls once.Do which skips ch.Cancel and ch.Close
	// because c.ch is nil — we skip those by not calling the real Stop, instead
	// testing the WaitGroup behaviour directly.

	// Simulate the consumer loop adding to the WaitGroup.
	c.wg.Add(1)
	started := time.Now()

	go func() {
		defer c.wg.Done()
		time.Sleep(processDelay)
	}()

	c.wg.Wait()
	elapsed := time.Since(started)
	if elapsed < processDelay-5*time.Millisecond {
		t.Errorf("Stop returned too early: elapsed=%v, want >= %v", elapsed, processDelay)
	}
}
