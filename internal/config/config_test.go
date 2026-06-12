package config

import (
	"strings"
	"testing"
)

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("ESV2_DB_DSN", "postgres://test/x")
	t.Setenv("ESV2_MQTT_BROKER_URL", "tcp://broker:1883")
	t.Setenv("ESV2_MQTT_TOPIC_PATTERN", "test/+")
	t.Setenv("ESV2_DEV_NO_AUTH", "true")
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
	if cfg.Auth.Enabled {
		t.Error("expected auth disabled with ESV2_DEV_NO_AUTH=true")
	}
}

func TestLoadMissingDSN(t *testing.T) {
	t.Setenv("ESV2_DB_DSN", "")
	t.Setenv("ESV2_MQTT_BROKER_URL", "tcp://broker:1883")
	t.Setenv("ESV2_MQTT_TOPIC_PATTERN", "test/+")
	t.Setenv("ESV2_DEV_NO_AUTH", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing dsn")
	}
}

func TestLoadAuthFailClosedDefault(t *testing.T) {
	// Without auth config and without the explicit dev override, Load
	// must refuse to start (audit 2026-06-12: default was auth-off).
	t.Setenv("ESV2_DB_DSN", "postgres://test/x")
	t.Setenv("ESV2_MQTT_BROKER_URL", "tcp://broker:1883")
	t.Setenv("ESV2_DEV_NO_AUTH", "")
	t.Setenv("ESV2_AUTH_APP_ISSUER", "")
	t.Setenv("ESV2_AUTH_APP_CLIENT_ID", "")
	_, err := Load()
	if err == nil {
		t.Fatal("expected fail-closed error without auth config")
	}
	if !strings.Contains(err.Error(), "ESV2_DEV_NO_AUTH") {
		t.Fatalf("error should point at the dev override, got: %v", err)
	}
}

func TestLoadAuthEnabledWithConfig(t *testing.T) {
	t.Setenv("ESV2_DB_DSN", "postgres://test/x")
	t.Setenv("ESV2_MQTT_BROKER_URL", "tcp://broker:1883")
	t.Setenv("ESV2_DEV_NO_AUTH", "")
	t.Setenv("ESV2_AUTH_APP_ISSUER", "https://kc.example.org/realms/x")
	t.Setenv("ESV2_AUTH_APP_CLIENT_ID", "energystore")
	t.Setenv("ESV2_AUTH_APP_AUDIENCE", "energystore-aud")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Auth.Enabled {
		t.Error("expected auth enabled by default")
	}
	if cfg.Auth.AppAudience != "energystore-aud" {
		t.Errorf("app audience: %q", cfg.Auth.AppAudience)
	}
}
