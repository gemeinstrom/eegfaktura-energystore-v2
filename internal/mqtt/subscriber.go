// Package mqtt implements the MQTT-5 Shared-Subscription consumer for
// energystore-v2. Multiple pods consume the same shared group; the broker
// distributes each message to exactly one subscriber.
package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/config"
)

// Handler is invoked per delivered MQTT message. Returning an error causes
// the message to be redirected to the DLQ instead of being silently dropped.
type Handler func(ctx context.Context, topic string, payload []byte) error

// Subscriber wraps a Paho MQTT 5 client subscribed to a Shared-Subscription
// topic. Topic pattern is expected to embed the share group, e.g.
//
//	$share/energystore/eegfaktura/+/energy/+
type Subscriber struct {
	client    mqtt.Client
	handler   Handler
	topic     string
	qos       byte
	connected atomic.Bool
	logger    *slog.Logger
	// onConnect / onDisconnect are exposed so the host can flip a gauge.
	onConnectionChange func(bool)
}

// Options is the optional knob bag accepted by NewWithOptions.
type Options struct {
	Logger             *slog.Logger
	OnConnectionChange func(bool)
}

// New is the legacy constructor; uses default logger + no-op gauge.
func New(cfg config.MQTTConfig, handler Handler, clientIDSuffix string) (*Subscriber, error) {
	return NewWithOptions(cfg, handler, clientIDSuffix, Options{})
}

// NewWithOptions constructs a Subscriber with logger and a callback for
// connection-state transitions.
func NewWithOptions(cfg config.MQTTConfig, handler Handler, clientIDSuffix string, opts Options) (*Subscriber, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "mqtt")

	s := &Subscriber{
		handler:            handler,
		qos:                cfg.QoS,
		logger:             logger,
		onConnectionChange: opts.OnConnectionChange,
	}

	clientOpts := mqtt.NewClientOptions().
		AddBroker(cfg.BrokerURL).
		SetClientID(cfg.ClientIDPrefix + clientIDSuffix).
		SetKeepAlive(time.Duration(cfg.KeepAlive) * time.Second).
		SetConnectTimeout(time.Duration(cfg.ConnectTimeout) * time.Second).
		SetCleanSession(false).
		SetOrderMatters(false).
		SetAutoReconnect(true).
		SetOnConnectHandler(func(_ mqtt.Client) {
			s.connected.Store(true)
			if s.onConnectionChange != nil {
				s.onConnectionChange(true)
			}
			logger.Info("connected", "broker", cfg.BrokerURL)
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			s.connected.Store(false)
			if s.onConnectionChange != nil {
				s.onConnectionChange(false)
			}
			logger.Warn("connection lost", "err", err)
		})

	topic := cfg.TopicPattern
	if topic == "" {
		return nil, fmt.Errorf("mqtt: topic pattern is required")
	}
	if cfg.ShareGroup != "" && topic[0] != '$' {
		topic = "$share/" + cfg.ShareGroup + "/" + topic
	}
	s.topic = topic
	s.client = mqtt.NewClient(clientOpts)
	return s, nil
}

// Connected reports the current broker connection state.
func (s *Subscriber) Connected() bool { return s.connected.Load() }

// Start connects to the broker and registers the subscription.
// Blocks until ctx is cancelled.
func (s *Subscriber) Start(ctx context.Context) error {
	if tok := s.client.Connect(); tok.Wait() && tok.Error() != nil {
		return fmt.Errorf("mqtt: connect: %w", tok.Error())
	}

	tok := s.client.Subscribe(s.topic, s.qos, func(_ mqtt.Client, m mqtt.Message) {
		if err := s.handler(ctx, m.Topic(), m.Payload()); err != nil {
			// The handler is responsible for DLQ writes; we log here for
			// debug visibility but do not unack — paho would re-deliver
			// indefinitely on the same payload.
			s.logger.Error("handler", "err", err, "topic", m.Topic())
		}
	})
	if tok.Wait() && tok.Error() != nil {
		s.client.Disconnect(250)
		return fmt.Errorf("mqtt: subscribe %q: %w", s.topic, tok.Error())
	}

	<-ctx.Done()
	s.client.Disconnect(250)
	s.connected.Store(false)
	if s.onConnectionChange != nil {
		s.onConnectionChange(false)
	}
	return ctx.Err()
}
