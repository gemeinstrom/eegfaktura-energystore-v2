// Package mqtt implements the MQTT-5 Shared-Subscription consumer for
// energystore-v2. Multiple pods consume the same shared group; the broker
// distributes each message to exactly one subscriber.
package mqtt

import (
	"context"
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/config"
)

// Handler is invoked per delivered MQTT message. Returning an error causes
// the message to not be ACKed (QoS 1 → broker redelivers).
type Handler func(ctx context.Context, topic string, payload []byte) error

// Subscriber wraps a Paho MQTT 5 client subscribed to a Shared-Subscription
// topic. Topic pattern is expected to embed the share group, e.g.
//
//	$share/energystore/eegfaktura/+/energy/+
type Subscriber struct {
	client  mqtt.Client
	handler Handler
	topic   string
	qos     byte
}

// New constructs a Subscriber. The topic is built from cfg.TopicPattern by
// prefixing $share/<group>/ when not already present.
func New(cfg config.MQTTConfig, handler Handler, clientIDSuffix string) (*Subscriber, error) {
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.BrokerURL).
		SetClientID(cfg.ClientIDPrefix + clientIDSuffix).
		SetKeepAlive(time.Duration(cfg.KeepAlive) * time.Second).
		SetConnectTimeout(time.Duration(cfg.ConnectTimeout) * time.Second).
		SetCleanSession(false).
		SetOrderMatters(false). // Shared-Sub: out-of-order across pods is normal
		SetAutoReconnect(true)

	topic := cfg.TopicPattern
	if topic == "" {
		return nil, fmt.Errorf("mqtt: topic pattern is required")
	}
	// Wrap into $share/<group>/<topic> if not already wrapped.
	if cfg.ShareGroup != "" && topic[0] != '$' {
		topic = "$share/" + cfg.ShareGroup + "/" + topic
	}

	return &Subscriber{
		client:  mqtt.NewClient(opts),
		handler: handler,
		topic:   topic,
		qos:     cfg.QoS,
	}, nil
}

// Start connects to the broker and registers the subscription.
// Blocks until ctx is cancelled.
func (s *Subscriber) Start(ctx context.Context) error {
	if tok := s.client.Connect(); tok.Wait() && tok.Error() != nil {
		return fmt.Errorf("mqtt: connect: %w", tok.Error())
	}

	tok := s.client.Subscribe(s.topic, s.qos, func(_ mqtt.Client, m mqtt.Message) {
		// The library acks for us when this callback returns without panic.
		// We log and drop errors here for now — production needs a
		// dead-letter strategy. TODO.
		if err := s.handler(ctx, m.Topic(), m.Payload()); err != nil {
			fmt.Printf("mqtt handler error: %v\n", err)
		}
	})
	if tok.Wait() && tok.Error() != nil {
		s.client.Disconnect(250)
		return fmt.Errorf("mqtt: subscribe %q: %w", s.topic, tok.Error())
	}

	<-ctx.Done()
	s.client.Disconnect(250)
	return ctx.Err()
}
