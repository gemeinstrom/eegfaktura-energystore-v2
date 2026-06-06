// Package mqtt implements the MQTT-5 Shared-Subscription consumer for
// energystore-v2. Multiple pods consume the same shared group; the broker
// distributes each message to exactly one subscriber.
//
// Uses github.com/eclipse/paho.golang (= "paho.golang", MQTT 5). The earlier
// implementation used github.com/eclipse/paho.mqtt.golang v1, which is
// MQTT 3.1.1-only. Shared Subscriptions are an MQTT 5 feature; mosquitto
// accepts the $share/group/topic subscribe from a 3.1.1 client silently
// but never matches a publish to it (verified pilot 2026-06-06). See
// feedback_paho_go_v1_shared_subscription.
package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/config"
)

// Handler is invoked per delivered MQTT message. Returning an error causes
// the message to be redirected to the DLQ instead of being silently dropped.
type Handler func(ctx context.Context, topic string, payload []byte) error

// Subscriber wraps an autopaho ConnectionManager subscribed to a
// Shared-Subscription topic. Topic pattern is expected to embed the share
// group, e.g.
//
//	$share/energystore/eegfaktura/+/energy/+
type Subscriber struct {
	cm                 *autopaho.ConnectionManager
	cfg                *autopaho.ClientConfig
	handler            Handler
	topic              string
	qos               byte
	connected          atomic.Bool
	logger             *slog.Logger
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
// connection-state transitions. The underlying ConnectionManager is created
// but not yet started — call Start(ctx) to connect.
func NewWithOptions(cfg config.MQTTConfig, handler Handler, clientIDSuffix string, opts Options) (*Subscriber, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "mqtt")

	if cfg.TopicPattern == "" {
		return nil, fmt.Errorf("mqtt: topic pattern is required")
	}
	if cfg.BrokerURL == "" {
		return nil, fmt.Errorf("mqtt: broker URL is required")
	}
	brokerURL, err := url.Parse(cfg.BrokerURL)
	if err != nil {
		return nil, fmt.Errorf("mqtt: parse broker URL %q: %w", cfg.BrokerURL, err)
	}

	topic := cfg.TopicPattern
	if cfg.ShareGroup != "" && topic[0] != '$' {
		topic = "$share/" + cfg.ShareGroup + "/" + topic
	}

	s := &Subscriber{
		handler:            handler,
		topic:              topic,
		qos:                cfg.QoS,
		logger:             logger,
		onConnectionChange: opts.OnConnectionChange,
	}

	clientID := cfg.ClientIDPrefix + clientIDSuffix

	keepAlive := uint16(cfg.KeepAlive)
	if keepAlive == 0 {
		keepAlive = 30
	}
	connectTimeout := time.Duration(cfg.ConnectTimeout) * time.Second
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}

	cliCfg := &autopaho.ClientConfig{
		ServerUrls:        []*url.URL{brokerURL},
		KeepAlive:         keepAlive,
		ConnectRetryDelay: 2 * time.Second,
		ConnectTimeout:    connectTimeout,
		// CleanStart=false so the broker preserves the (clientID,
		// subscription) tuple across reconnects — same intent as the
		// old SetCleanSession(false).
		CleanStartOnInitialConnection: false,
		// SessionExpiryInterval in seconds; ~1h is plenty for transient
		// network blips while keeping queued-message state bounded.
		SessionExpiryInterval: 3600,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			s.connected.Store(true)
			if s.onConnectionChange != nil {
				s.onConnectionChange(true)
			}
			logger.Info("connected", "broker", cfg.BrokerURL)

			// Subscribe on each connect — autopaho re-fires this on
			// reconnect, so we re-establish the subscription if the
			// broker forgot it.
			ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
			defer cancel()
			if _, err := cm.Subscribe(ctx, &paho.Subscribe{
				Subscriptions: []paho.SubscribeOptions{
					{Topic: topic, QoS: cfg.QoS, NoLocal: true},
				},
			}); err != nil {
				logger.Error("subscribe", "err", err, "topic", topic)
			}
		},
		OnConnectError: func(err error) {
			s.connected.Store(false)
			if s.onConnectionChange != nil {
				s.onConnectionChange(false)
			}
			logger.Warn("connection error", "err", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: clientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					if err := s.handler(context.Background(), pr.Packet.Topic, pr.Packet.Payload); err != nil {
						// The handler is responsible for DLQ writes; we log
						// here for debug visibility but DO return true so
						// autopaho ACKs the message — returning an error
						// here would cause infinite re-delivery on the
						// same payload (matches v1 behaviour).
						logger.Error("handler", "err", err, "topic", pr.Packet.Topic)
					}
					return true, nil
				},
			},
			OnClientError: func(err error) {
				logger.Warn("client error", "err", err)
			},
			OnServerDisconnect: func(d *paho.Disconnect) {
				s.connected.Store(false)
				if s.onConnectionChange != nil {
					s.onConnectionChange(false)
				}
				logger.Warn("server disconnect", "reason_code", d.ReasonCode)
			},
		},
	}

	s.cfg = cliCfg
	return s, nil
}

// Connected reports the current broker connection state.
func (s *Subscriber) Connected() bool { return s.connected.Load() }

// Start connects to the broker and registers the subscription.
// Blocks until ctx is cancelled.
func (s *Subscriber) Start(ctx context.Context) error {
	cm, err := autopaho.NewConnection(ctx, *s.cfg)
	if err != nil {
		return fmt.Errorf("mqtt: new connection: %w", err)
	}
	s.cm = cm

	// Wait for initial connection to surface configuration errors early.
	connectCtx, cancel := context.WithTimeout(ctx, s.cfg.ConnectTimeout)
	defer cancel()
	if err := cm.AwaitConnection(connectCtx); err != nil {
		return fmt.Errorf("mqtt: await connection: %w", err)
	}

	<-ctx.Done()

	// Best-effort graceful disconnect.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = cm.Disconnect(shutdownCtx)

	s.connected.Store(false)
	if s.onConnectionChange != nil {
		s.onConnectionChange(false)
	}
	return ctx.Err()
}
