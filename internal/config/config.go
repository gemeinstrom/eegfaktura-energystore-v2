// Package config wraps configuration loading (env + file) for energystore-v2.
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	HTTP HTTPConfig
	DB   DBConfig
	MQTT MQTTConfig
	Auth AuthConfig
}

type AuthConfig struct {
	// AppIssuer / AppClientID are used by ProtectApp + GQL (bearer JWT
	// verify against the customer realm).
	AppIssuer   string
	AppClientID string

	// APIIssuer / APIClientID / APIClientSecret drive the password-grant
	// bridge used by ProtectAPI (Basic-Auth).
	APIIssuer       string
	APIClientID     string
	APIClientSecret string

	// Enabled gates whether main wires Protect* into the API. Off by
	// default so dev environments without Keycloak still come up.
	Enabled bool
}

type HTTPConfig struct {
	ListenAddr string
}

type DBConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	AppName         string
	StatementCache  bool
}

type MQTTConfig struct {
	BrokerURL       string
	ClientIDPrefix  string
	ShareGroup      string
	TopicPattern    string
	QoS             byte
	KeepAlive       int
	ConnectTimeout  int

	// Inverter is the second subscription for PV-Wechselrichter-Daten.
	// Disabled (empty TopicPattern) by default; mirrors v1's separate
	// `mqtt.inverterSubscriptionTopic`.
	Inverter MQTTInverterConfig
}

type MQTTInverterConfig struct {
	// TopicPattern enables the inverter subscription when non-empty.
	TopicPattern string
	ShareGroup   string
}

// Load reads configuration from viper sources (config file + env).
// Env vars override file values.
func Load() (*Config, error) {
	viper.SetEnvPrefix("ESV2")
	// Without an EnvKeyReplacer, viper looks for env var "ESV2_DB.DSN"
	// when the config key is "db.dsn" — the literal dot prevents the
	// env lookup. Map dots and dashes to underscores so ESV2_DB_DSN
	// resolves to db.dsn (and ESV2_MQTT_INVERTER_TOPIC_PATTERN to
	// mqtt.inverter.topic_pattern).
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	viper.AutomaticEnv()

	viper.SetDefault("http.listen_addr", ":8080")
	viper.SetDefault("db.max_conns", int32(16))
	viper.SetDefault("db.min_conns", int32(2))
	viper.SetDefault("db.app_name", "energystore-v2")
	viper.SetDefault("mqtt.client_id_prefix", "energystore-v2-")
	viper.SetDefault("mqtt.share_group", "energystore")
	viper.SetDefault("mqtt.qos", byte(1))
	viper.SetDefault("mqtt.keep_alive", 30)
	viper.SetDefault("mqtt.connect_timeout", 10)
	viper.SetDefault("mqtt.inverter.share_group", "energystore-inverter")
	viper.SetDefault("auth.enabled", false)

	// viper's AutomaticEnv only binds env vars for keys it knows about.
	// Keys that don't have a SetDefault must be BindEnv'd explicitly,
	// otherwise GetString("db.dsn") returns "" even when ESV2_DB_DSN is
	// set in the environment.
	for _, key := range []string{
		"db.dsn",
		"db.statement_cache",
		"mqtt.broker_url",
		"mqtt.topic_pattern",
		"mqtt.inverter.topic_pattern",
		"auth.app_issuer", "auth.app_client_id",
		"auth.api_issuer", "auth.api_client_id", "auth.api_client_secret",
	} {
		_ = viper.BindEnv(key)
	}

	if err := viper.ReadInConfig(); err != nil {
		if _, notFound := err.(viper.ConfigFileNotFoundError); !notFound {
			return nil, fmt.Errorf("config: read: %w", err)
		}
	}

	cfg := &Config{
		HTTP: HTTPConfig{
			ListenAddr: viper.GetString("http.listen_addr"),
		},
		DB: DBConfig{
			DSN:            viper.GetString("db.dsn"),
			MaxConns:       viper.GetInt32("db.max_conns"),
			MinConns:       viper.GetInt32("db.min_conns"),
			AppName:        viper.GetString("db.app_name"),
			StatementCache: viper.GetBool("db.statement_cache"),
		},
		MQTT: MQTTConfig{
			BrokerURL:      viper.GetString("mqtt.broker_url"),
			ClientIDPrefix: viper.GetString("mqtt.client_id_prefix"),
			ShareGroup:     viper.GetString("mqtt.share_group"),
			TopicPattern:   viper.GetString("mqtt.topic_pattern"),
			QoS:            byte(viper.GetInt("mqtt.qos")),
			KeepAlive:      viper.GetInt("mqtt.keep_alive"),
			ConnectTimeout: viper.GetInt("mqtt.connect_timeout"),
			Inverter: MQTTInverterConfig{
				TopicPattern: viper.GetString("mqtt.inverter.topic_pattern"),
				ShareGroup:   viper.GetString("mqtt.inverter.share_group"),
			},
		},
		Auth: AuthConfig{
			Enabled:         viper.GetBool("auth.enabled"),
			AppIssuer:       viper.GetString("auth.app_issuer"),
			AppClientID:     viper.GetString("auth.app_client_id"),
			APIIssuer:       viper.GetString("auth.api_issuer"),
			APIClientID:     viper.GetString("auth.api_client_id"),
			APIClientSecret: viper.GetString("auth.api_client_secret"),
		},
	}

	if cfg.DB.DSN == "" {
		return nil, fmt.Errorf("config: db.dsn is required")
	}
	if cfg.MQTT.BrokerURL == "" {
		return nil, fmt.Errorf("config: mqtt.broker_url is required")
	}
	if cfg.Auth.Enabled && (cfg.Auth.AppIssuer == "" || cfg.Auth.AppClientID == "") {
		return nil, fmt.Errorf("config: auth.enabled requires auth.app_issuer + auth.app_client_id")
	}
	return cfg, nil
}
