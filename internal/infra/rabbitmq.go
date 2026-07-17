package infra

import (
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQ holds an AMQP connection and a single channel used for health checks.
// Business-logic channels are opened per-operation in later stages.
type RabbitMQ struct {
	conn    *amqp.Connection
	Channel *amqp.Channel
}

// NewRabbitMQ dials RabbitMQ and opens a channel to verify connectivity.
// url must not be logged by the caller.
func NewRabbitMQ(url string) (*RabbitMQ, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: open channel: %w", err)
	}

	slog.Info("rabbitmq: connected")
	return &RabbitMQ{conn: conn, Channel: ch}, nil
}

// Close closes the channel and the underlying connection in order.
func (r *RabbitMQ) Close() error {
	if err := r.Channel.Close(); err != nil {
		return fmt.Errorf("rabbitmq: close channel: %w", err)
	}
	if err := r.conn.Close(); err != nil {
		return fmt.Errorf("rabbitmq: close connection: %w", err)
	}
	return nil
}

// NewChannel opens a new AMQP channel on the existing connection.
// The caller is responsible for closing the returned channel.
func (r *RabbitMQ) NewChannel() (*amqp.Channel, error) {
	ch, err := r.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: new channel: %w", err)
	}
	return ch, nil
}

// IsHealthy returns true when the channel is open and not closed.
func (r *RabbitMQ) IsHealthy() bool {
	return !r.Channel.IsClosed()
}
