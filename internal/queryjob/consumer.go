package queryjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

const consumerTag = "worker-query-consumer"

// deliveryOutcome instructs the run loop how to respond to a delivery.
type deliveryOutcome int

const (
	outcomeAck           deliveryOutcome = iota // ACK: terminal state persisted
	outcomeNackNoRequeue                        // NACK, do not requeue (bad message or missing job)
	outcomeFatal                                // do not ACK; stop the consumer
)

// Consumer subscribes to the query execution queue and delegates each message
// to a ProcessService. It is safe to call Stop concurrently with Start.
type Consumer struct {
	ch   *amqp.Channel
	svc  ProcessService
	wg   sync.WaitGroup
	once sync.Once
}

// NewConsumer declares the MQ topology on ch and returns a Consumer ready to
// be started. ch must be a dedicated channel; it must not be shared with the
// publisher or health check.
func NewConsumer(ch *amqp.Channel, svc ProcessService) (*Consumer, error) {
	if err := declareMQTopology(ch); err != nil {
		return nil, fmt.Errorf("consumer: declare topology: %w", err)
	}
	if err := ch.Qos(1, 0, false); err != nil {
		return nil, fmt.Errorf("consumer: set qos: %w", err)
	}
	return &Consumer{ch: ch, svc: svc}, nil
}

// Start begins consuming messages in a background goroutine. It returns
// immediately; use Stop to signal shutdown and wait for in-flight work.
func (c *Consumer) Start() error {
	deliveries, err := c.ch.Consume(
		mqQueue, consumerTag,
		false, // autoAck
		false, false, false, nil,
	)
	if err != nil {
		return fmt.Errorf("consumer: start consume: %w", err)
	}

	go func() {
		for d := range deliveries {
			c.wg.Add(1)
			outcome, err := c.handle(d)
			c.wg.Done()

			switch outcome {
			case outcomeAck:
				if ackErr := d.Ack(false); ackErr != nil {
					slog.Error("consumer: ack failed", "err", ackErr)
				}
			case outcomeNackNoRequeue:
				if nackErr := d.Nack(false, false); nackErr != nil {
					slog.Error("consumer: nack failed", "err", nackErr)
				}
			case outcomeFatal:
				// Do not ACK. Close the channel so the broker requeues the
				// unacked message. The range loop will exit when the channel
				// is closed.
				slog.Error("consumer: fatal error, closing channel", "err", err)
				c.once.Do(func() { c.ch.Close() })
				return
			}
		}
		slog.Info("consumer: delivery channel closed, exiting")
	}()

	return nil
}

// Stop cancels the consumer subscription, waits for any in-flight message to
// complete, then closes the channel. It is idempotent.
func (c *Consumer) Stop() {
	c.once.Do(func() {
		// Stop new deliveries. noWait=false: wait for broker confirmation.
		if err := c.ch.Cancel(consumerTag, false); err != nil {
			slog.Warn("consumer: cancel failed", "err", err)
		}
		c.wg.Wait()
		if err := c.ch.Close(); err != nil {
			slog.Warn("consumer: close channel failed", "err", err)
		}
	})
}

// handle processes a single delivery and returns the appropriate outcome.
// It never calls Ack/Nack itself; the caller is responsible.
func (c *Consumer) handle(d amqp.Delivery) (deliveryOutcome, error) {
	// Parse the message envelope.
	var msg QueryJobMessage
	if err := json.Unmarshal(d.Body, &msg); err != nil {
		slog.Error("consumer: malformed message", "err", err)
		return outcomeNackNoRequeue, err
	}

	// Validate envelope fields.
	if msg.Type != MessageTypeQueryExecutionRequested || msg.Version != messageVersion {
		slog.Error("consumer: unsupported message type or version",
			"type", msg.Type, "version", msg.Version)
		return outcomeNackNoRequeue, fmt.Errorf("unsupported type=%s version=%d", msg.Type, msg.Version)
	}
	if msg.Payload.JobID == 0 {
		slog.Error("consumer: invalid job_id in message")
		return outcomeNackNoRequeue, fmt.Errorf("invalid job_id=0")
	}

	// Delegate to the service.
	err := c.svc.Process(context.Background(), msg.Payload.JobID)
	if err == nil {
		return outcomeAck, nil
	}

	// Job not found or stale: discard without requeue.
	if errors.Is(err, ErrJobNotFound) {
		slog.Error("consumer: job not found", "job_id", msg.Payload.JobID)
		return outcomeNackNoRequeue, err
	}

	// MySQL write failure or unexpected status conflict: stop the consumer so
	// the unacked message is requeued by the broker on channel close.
	slog.Error("consumer: fatal process error, stopping consumer", "job_id", msg.Payload.JobID, "err", err)
	return outcomeFatal, err
}
