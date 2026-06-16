// Package eventing publishes group-change events over AMQP so downstream
// consumers (such as the search indexer) can react to changes. The messages
// match those emitted by iplant-groups: a routing key of "index.group.<id>" and
// a JSON body of {"message":"","timestamp_ms":<ms>}.
package eventing

import "context"

// Publisher publishes group-change events.
type Publisher interface {
	// GroupChanged signals that the group with the given ID was created,
	// updated, deleted, or had its membership changed.
	GroupChanged(ctx context.Context, groupID string) error

	// Close releases any resources held by the publisher.
	Close() error
}

// NoopPublisher discards events. It is used when AMQP is not configured.
type NoopPublisher struct{}

// GroupChanged does nothing and returns nil.
func (NoopPublisher) GroupChanged(context.Context, string) error { return nil }

// Close does nothing and returns nil.
func (NoopPublisher) Close() error { return nil }
