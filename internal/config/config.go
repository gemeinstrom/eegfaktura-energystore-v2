// Package config wraps configuration loading (env + file) for energystore-v2.
package config

import (
	"fmt"

	"github.com/spf13/viper"
)

type Config struct {
	HTTP HTTPConfig
	DB   DBConfig
	MQTT MQTTConfig
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
}

// Load reads configuration from viper sources (config file + env).
// Env vars override file values.
func Load() (*Config, error) {
	viper.SetEnvPrefix("ESV2")
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
		},
	}

	if cfg.DB.DSN == "" {
		return nil, fmt.Errorf("config: db.dsn is required")
	}
	if cfg.MQTT.BrokerURL == "" {
		return nil, fmt.Errorf("config: mqtt.broker_url is required")
	}
	return cfg, nil
}
