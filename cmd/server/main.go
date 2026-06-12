// Command energystore-v2 is the stateless time-series ingest+API service
// for the eegfaktura platform. See ../../README.md for the architectural
// rationale.
//
// Subcommands:
//
//	energystore-v2          → runs the serve loop (default)
//	energystore-v2 serve    → same as default
//	energystore-v2 migrate  → applies embedded SQL migrations and exits
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/cors"
	"golang.org/x/sync/errgroup"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/api"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/auth"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/calc"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/config"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/decode"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/excelexport"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/graphqlapi"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/logging"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/metrics"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/migrate"
	mqttsub "github.com/gemeinstrom/eegfaktura-energystore-v2/internal/mqtt"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

func main() {
	logger := logging.Setup()

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := runServe(logger); err != nil {
			logger.Error("serve exited with error", "err", err)
			os.Exit(1)
		}
	case "migrate":
		if err := runMigrate(logger); err != nil {
			logger.Error("migrate exited with error", "err", err)
			os.Exit(1)
		}
	default:
		logger.Error("unknown subcommand", "cmd", cmd)
		os.Exit(2)
	}
}

func runMigrate(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	logger.Info("migrate: applying embedded migrations")
	if err := migrate.Run(ctx, cfg.DB.DSN); err != nil {
		return err
	}
	logger.Info("migrate: complete")
	return nil
}

func runServe(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st, err := store.New(ctx, cfg.DB.DSN, cfg.DB.MinConns, cfg.DB.MaxConns, cfg.DB.AppName)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()

	mtr := metrics.New()
	mtr.MQTTConnected.Set(0)

	authMW, err := buildAuth(ctx, cfg.Auth, logger)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	cpRepo := counterpoint.NewRepository(store.RawPool(st))
	qeEngine := queryengine.New(store.RawPool(st), cpRepo)
	calcEngine := calc.New(store.RawPool(st), cpRepo)
	excelEngine := excelexport.New(qeEngine, cpRepo)
	gqlEngine, err := graphqlapi.New(st, calcEngine, qeEngine, cpRepo, graphqlapi.Options{Logger: logger})
	if err != nil {
		return fmt.Errorf("graphql: %w", err)
	}

	// decryptCfg holds the optional pre-decode step that unwraps the
	// prod-stack's AES-256-CBC + gzip + base64 envelope. Empty in the
	// pilot (= pass-through). Enabled via ESV2_MQTT_DECRYPT_KEY_HEX +
	// ESV2_MQTT_DECRYPT_IV_HEX + ESV2_MQTT_DECRYPT_GZIP=true.
	decryptCfg := decode.DecryptConfig{
		Key:  cfg.MQTT.Decrypt.Key,
		IV:   cfg.MQTT.Decrypt.IV,
		Gzip: cfg.MQTT.Decrypt.Gzip,
	}
	if decryptCfg.Enabled() {
		logger.Info("mqtt: payload decrypt enabled",
			"key_bytes", len(decryptCfg.Key),
			"iv_bytes", len(decryptCfg.IV),
			"gzip", decryptCfg.Gzip)
	}

	// energyHandler ingests EDA energy-data messages: tenant from topic,
	// ecId from payload.
	energyHandler := makeIngestHandler(st, mtr, logger, "energy", "", decryptCfg)
	// inverterHandler ingests PV-inverter messages: tenant from topic,
	// ecId hard-coded to "inverter" (mirrors v1 importEnergyV2 call
	// site). Inverter messages are not encrypted in the prod stack
	// (the encrypt-only-CR_MSG check), so pass an empty config to
	// short-circuit the decode pre-step.
	inverterHandler := makeIngestHandler(st, mtr, logger, "inverter", "inverter", decode.DecryptConfig{})

	hostname, _ := os.Hostname()
	energySub, err := mqttsub.NewWithOptions(cfg.MQTT, energyHandler, hostname+"-energy", mqttsub.Options{
		Logger: logger,
		OnConnectionChange: func(connected bool) {
			if connected {
				mtr.MQTTConnected.Set(1)
			} else {
				mtr.MQTTConnected.Set(0)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("mqtt energy: %w", err)
	}

	// Optional second subscription for PV-inverter messages. Disabled
	// unless cfg.MQTT.Inverter.TopicPattern is set.
	var inverterSub *mqttsub.Subscriber
	if cfg.MQTT.Inverter.TopicPattern != "" {
		invCfg := cfg.MQTT
		invCfg.TopicPattern = cfg.MQTT.Inverter.TopicPattern
		invCfg.ShareGroup = cfg.MQTT.Inverter.ShareGroup
		inverterSub, err = mqttsub.NewWithOptions(invCfg, inverterHandler, hostname+"-inverter", mqttsub.Options{
			Logger: logger,
		})
		if err != nil {
			return fmt.Errorf("mqtt inverter: %w", err)
		}
	}

	apiSrv := api.NewWithOptions(st, api.Options{
		Logger:      logger,
		Metrics:     mtr,
		MQTT:        energySub,
		Auth:        authMW,
		QueryEngine: qeEngine,
		Calc:        calcEngine,
		Excel:       excelEngine,
		GraphQL:     gqlEngine,
	})

	corsHandler := cors.New(cors.Options{
		AllowedOrigins: corsOriginsFromEnv(),
		AllowedMethods: []string{"GET", "HEAD", "POST", "PUT", "OPTIONS", "DELETE"},
		AllowedHeaders: []string{
			"Accept", "Accept-Encoding", "Accept-Language",
			"Authorization", "Content-Type", "Content-Length",
			"Origin", "User-Agent", "X-Requested-With", "X-Tenant",
		},
		AllowCredentials: true,
	}).Handler(apiSrv.Handler())

	httpSrv := &http.Server{
		Addr:              cfg.HTTP.ListenAddr,
		Handler:           corsHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		logger.Info("http: listening", "addr", cfg.HTTP.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		logger.Info("mqtt: starting energy subscriber")
		if err := energySub.Start(gctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("mqtt energy: %w", err)
		}
		return nil
	})

	if inverterSub != nil {
		g.Go(func() error {
			logger.Info("mqtt: starting inverter subscriber",
				"topic", cfg.MQTT.Inverter.TopicPattern)
			if err := inverterSub.Start(gctx); err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("mqtt inverter: %w", err)
			}
			return nil
		})
	}

	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		return httpSrv.Shutdown(shutdownCtx)
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// makeIngestHandler returns the MQTT subscription callback that decodes
// payload, runs UpsertSlots, and emits metrics + DLQ for failures.
// The `source` label distinguishes energy vs inverter in metrics; the
// optional `ecidOverride` (used only by the inverter pipeline — mirrors
// v1's hard-coded "inverter" ecId in calculation/mqttimporter.go:54) is
// applied to every slot after decode so the row lands in a deterministic
// (tenant, "inverter") namespace.
func makeIngestHandler(st *store.Store, mtr *metrics.Metrics, logger *slog.Logger,
	source, ecidOverride string, decryptCfg decode.DecryptConfig) mqttsub.Handler {
	return func(hctx context.Context, topic string, payload []byte) error {
		start := time.Now()
		defer func() {
			mtr.MQTTUpsertDuration.Observe(time.Since(start).Seconds())
		}()

		tenant := tenantFromTopic(topic)
		if tenant == "" {
			err := fmt.Errorf("cannot extract tenant from topic %q", topic)
			writeDLQ(hctx, st, mtr, logger, topic, "decode", err.Error(), payload)
			mtr.MQTTMessagesTotal.WithLabelValues(source, "decode_error").Inc()
			mtr.MQTTDecodeErrors.Inc()
			return err
		}

		// Optional decrypt: pass-through when DecryptConfig is empty
		// (pilot default). Prod-cutover sets ESV2_MQTT_DECRYPT_KEY_HEX
		// etc. to enable AES-256-CBC + gzip + base64 unwrap.
		raw, err := decryptCfg.Decrypt(payload)
		if err != nil {
			writeDLQ(hctx, st, mtr, logger, topic, "decrypt", err.Error(), payload)
			mtr.MQTTMessagesTotal.WithLabelValues(source, "decrypt_error").Inc()
			mtr.MQTTDecodeErrors.Inc()
			return fmt.Errorf("decrypt: %w", err)
		}

		slots, err := decode.DecodeSlots(tenant, raw)
		if err != nil {
			writeDLQ(hctx, st, mtr, logger, topic, "decode", err.Error(), payload)
			mtr.MQTTMessagesTotal.WithLabelValues(source, "decode_error").Inc()
			mtr.MQTTDecodeErrors.Inc()
			return fmt.Errorf("decode: %w", err)
		}

		if ecidOverride != "" {
			for i := range slots {
				slots[i].ECID = ecidOverride
			}
		}

		mtr.MQTTUpsertBatchSize.Observe(float64(len(slots)))

		if len(slots) == 0 {
			mtr.MQTTMessagesTotal.WithLabelValues(source, "ok").Inc()
			return nil
		}
		if err := st.UpsertSlots(hctx, slots); err != nil {
			writeDLQ(hctx, st, mtr, logger, topic, "upsert", err.Error(), payload)
			mtr.MQTTMessagesTotal.WithLabelValues(source, "upsert_error").Inc()
			mtr.MQTTUpsertErrors.Inc()
			return fmt.Errorf("upsert: %w", err)
		}
		mtr.MQTTMessagesTotal.WithLabelValues(source, "ok").Inc()
		return nil
	}
}

// writeDLQ logs + writes the failed payload, swallowing DLQ-write errors
// (DLQ failure must not block ack of the original message — paho would
// just redeliver and we'd hot-loop).
func writeDLQ(ctx context.Context, st *store.Store, mtr *metrics.Metrics, logger *slog.Logger,
	topic, failure, errMsg string, payload []byte) {
	if err := st.WriteDLQ(ctx, topic, failure, errMsg, payload); err != nil {
		logger.Error("dlq write failed", "err", err, "topic", topic, "failure", failure)
		return
	}
	mtr.MQTTDLQWrites.Inc()
}

// buildAuth constructs the auth middleware bundle. Auth is on by default
// (fail-closed, see config.Load); the only way to run without it is the
// explicit dev override ESV2_DEV_NO_AUTH=true, which returns nil — the
// REST endpoints will then run unauthenticated. When enabled, the App
// issuer is mandatory (config.Load already rejects missing values); the
// API issuer is optional (some clusters don't expose ProtectAPI).
func buildAuth(ctx context.Context, cfg config.AuthConfig, logger *slog.Logger) (*auth.Middleware, error) {
	if !cfg.Enabled {
		if !cfg.DevNoAuth {
			// Defensive: config.Load only clears Enabled via DevNoAuth,
			// but fail closed if that invariant ever breaks.
			return nil, fmt.Errorf("auth disabled without ESV2_DEV_NO_AUTH=true")
		}
		logger.Warn("auth DISABLED via ESV2_DEV_NO_AUTH=true — endpoints will be unprotected (dev only, never in production)")
		return nil, nil
	}
	appKC, err := auth.NewKeycloakClient(ctx, cfg.AppIssuer, cfg.AppClientID, "", cfg.AppAudience, nil)
	if err != nil {
		return nil, fmt.Errorf("app KC: %w", err)
	}
	var apiKC *auth.KeycloakClient
	if cfg.APIIssuer != "" && cfg.APIClientID != "" {
		apiKC, err = auth.NewKeycloakClient(ctx, cfg.APIIssuer, cfg.APIClientID, cfg.APIClientSecret, cfg.APIAudience, nil)
		if err != nil {
			return nil, fmt.Errorf("api KC: %w", err)
		}
	}
	if cfg.AppAudience == "" {
		logger.Warn("auth: no ESV2_AUTH_APP_AUDIENCE set — JWT audience (aud) is NOT verified (v1-compat)")
	}
	logger.Info("auth enabled", "app_issuer", cfg.AppIssuer, "api_issuer", cfg.APIIssuer,
		"app_audience", cfg.AppAudience, "api_audience", cfg.APIAudience)
	return auth.FromKeycloak(appKC, apiKC, auth.Options{Logger: logger}), nil
}

// tenantFromTopic extracts the tenant from a broker topic of the form
// `eegfaktura/<tenant>/energy/<...>`. Returns "" if the topic doesn't
// match the expected shape.
func tenantFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// corsOriginsFromEnv returns the comma-split ESV2_CORS_ALLOWED_ORIGINS,
// defaulting to ["*"] when unset. v1 used "*" via gorilla/handlers; same
// default here.
func corsOriginsFromEnv() []string {
	raw := os.Getenv("ESV2_CORS_ALLOWED_ORIGINS")
	if raw == "" {
		return []string{"*"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}
