package queryjob

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	mqExchange   = "askdb.events"
	mqQueue      = "askdb.query.execution"
	mqRoutingKey = "query.execution.requested"
)

// Publisher publishes query job messages to the message broker.
// The interface is declared here (consumer side) so the Service can be tested
// without a real RabbitMQ connection.
type Publisher interface {
	Publish(ctx context.Context, jobID uint64) error
	Close() error
}

// RabbitMQPublisher sends query job messages to RabbitMQ using a dedicated
// channel. It is safe for concurrent use by multiple goroutines.
type RabbitMQPublisher struct {
	ch   *amqp.Channel
	mu   sync.Mutex
	once sync.Once
}

// NewRabbitMQPublisher declares the exchange, queue, and binding, sets QoS,
// and returns a publisher ready for use. ch must be a dedicated channel; it
// must not be shared with health checks or consumers.
func NewRabbitMQPublisher(ch *amqp.Channel) (*RabbitMQPublisher, error) {
	if err := declareMQTopology(ch); err != nil {
		return nil, fmt.Errorf("publisher: declare topology: %w", err)
	}
	return &RabbitMQPublisher{ch: ch}, nil
}

// Publish serialises a QueryJobMessage and sends it to the exchange.
// The message contains only the job_id and envelope metadata; no sensitive
// data (question, SQL, DSN, credentials) is included.
//
// Concurrent calls are serialised via an internal mutex because a single
// AMQP channel must not be used concurrently.
//
// NOTE: PublishWithContext returning nil does not guarantee broker persistence.
// Publisher Confirm is deferred to a later reliability phase.
func (p *RabbitMQPublisher) Publish(ctx context.Context, jobID uint64) error {
	msg := QueryJobMessage{
		MessageID:  newMessageID(),
		Type:       MessageTypeQueryExecutionRequested,
		Version:    messageVersion,
		OccurredAt: time.Now().UTC(),
		Payload:    JobPayload{JobID: jobID},
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("publisher: marshal: %w", err)
	}

	pub := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    msg.MessageID,
		Type:         msg.Type,
		Timestamp:    msg.OccurredAt,
		Body:         body,
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ch.PublishWithContext(ctx, mqExchange, mqRoutingKey, false, false, pub); err != nil {
		return fmt.Errorf("publisher: publish job_id=%d: %w", jobID, err)
	}
	return nil
}

// Close closes the underlying AMQP channel. It is idempotent.
func (p *RabbitMQPublisher) Close() error {
	var err error
	p.once.Do(func() {
		if closeErr := p.ch.Close(); closeErr != nil {
			err = fmt.Errorf("publisher: close channel: %w", closeErr)
		}
	})
	return err
}

// declareMQTopology idempotently declares the exchange, queue, and binding
// used by all producers and consumers in this application.
func declareMQTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(
		mqExchange, "direct",
		true, false, false, false, nil,
	); err != nil {
		return fmt.Errorf("exchange declare: %w", err)
	}

	if _, err := ch.QueueDeclare(
		mqQueue,
		true, false, false, false, nil,
	); err != nil {
		return fmt.Errorf("queue declare: %w", err)
	}

	if err := ch.QueueBind(mqQueue, mqRoutingKey, mqExchange, false, nil); err != nil {
		return fmt.Errorf("queue bind: %w", err)
	}
	return nil
}

// newMessageID returns a random 16-byte hex string suitable for use as a
// unique message identifier. crypto/rand is used; no UUID dependency needed.
func newMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely unlikely; fall back to a
		// timestamp-based value rather than panicking.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}
