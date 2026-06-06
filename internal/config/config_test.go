package config

import (
	"testing"
)

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("ESV2_DB_DSN", "postgres://test/x")
	t.Setenv("ESV2_MQTT_BROKER_URL", "tcp://broker:1883")
	t.Setenv("ESV2_MQTT_TOPIC_PATTERN", "test/+")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DB.DSN != "postgres://test/x" {
		t.Errorf("dsn: %q", cfg.DB.DSN)
	}
	if cfg.MQTT.BrokerURL != "tcp://broker:1883" {
		t.Errorf("broker: %q", cfg.MQTT.BrokerURL)
	}
	if cfg.MQTT.TopicPattern != "test/+" {
		t.Errorf("topic: %q", cfg.MQTT.TopicPattern)
	}
}

func TestLoadMissingDSN(t *testing.T) {
	t.Setenv("ESV2_DB_DSN", "")
	t.Setenv("ESV2_MQTT_BROKER_URL", "tcp://broker:1883")
	t.Setenv("ESV2_MQTT_TOPIC_PATTERN", "test/+")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing dsn")
	}
}
