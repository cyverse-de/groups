package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	messaging "github.com/cyverse-de/messaging/v12"
)

// routingKeyPrefix is prepended to a group ID to form the AMQP routing key,
// matching the convention used by iplant-groups.
const routingKeyPrefix = "index.group."

// amqpPublisher publishes group-change events to a RabbitMQ topic exchange.
type amqpPublisher struct {
	client *messaging.Client
}

// NewAMQPPublisher connects to the broker at uri and prepares to publish to the
// named topic exchange.
func NewAMQPPublisher(uri, exchange string) (Publisher, error) {
	client, err := messaging.NewClient(uri, true)
	if err != nil {
		return nil, fmt.Errorf("eventing: connecting to AMQP: %w", err)
	}
	if err := client.SetupPublishing(exchange); err != nil {
		client.Close()
		return nil, fmt.Errorf("eventing: setting up publishing on exchange %q: %w", exchange, err)
	}
	return &amqpPublisher{client: client}, nil
}

// GroupChanged publishes a reindex message for the given group.
func (p *amqpPublisher) GroupChanged(ctx context.Context, groupID string) error {
	body, err := json.Marshal(map[string]any{
		"message":      "",
		"timestamp_ms": time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	return p.client.PublishContextOpts(ctx, routingKeyPrefix+groupID, body, messaging.JSONPublishingOpts)
}

// Close shuts down the underlying messaging client.
func (p *amqpPublisher) Close() error {
	p.client.Close()
	return nil
}
