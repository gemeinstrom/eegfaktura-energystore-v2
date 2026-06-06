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
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/api"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/config"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/decode"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/migrate"
	mqttsub "github.com/gemeinstrom/eegfaktura-energystore-v2/internal/mqtt"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := runServe(); err != nil {
			log.Fatalf("energystore-v2 serve: %v", err)
		}
	case "migrate":
		if err := runMigrate(); err != nil {
			log.Fatalf("energystore-v2 migrate: %v", err)
		}
	default:
		log.Fatalf("energystore-v2: unknown subcommand %q (expected: serve, migrate)", cmd)
	}
}

func runMigrate() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Print("migrate: applying embedded migrations")
	if err := migrate.Run(ctx, cfg.DB.DSN); err != nil {
		return err
	}
	log.Print("migrate: complete")
	return nil
}

func runServe() error {
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

	handler := func(hctx context.Context, topic string, payload []byte) error {
		tenant := tenantFromTopic(topic)
		if tenant == "" {
			return fmt.Errorf("ingest: cannot extract tenant from topic %q", topic)
		}
		slots, err := decode.DecodeSlots(tenant, payload)
		if err != nil {
			return fmt.Errorf("ingest: decode: %w", err)
		}
		if len(slots) == 0 {
			return nil
		}
		if err := st.UpsertSlots(hctx, slots); err != nil {
			return fmt.Errorf("ingest: upsert: %w", err)
		}
		return nil
	}

	hostname, _ := os.Hostname()
	sub, err := mqttsub.New(cfg.MQTT, handler, hostname)
	if err != nil {
		return fmt.Errorf("mqtt: %w", err)
	}

	apiSrv := api.New(st)
	httpSrv := &http.Server{
		Addr:              cfg.HTTP.ListenAddr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Printf("http: listening on %s", cfg.HTTP.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		log.Printf("mqtt: starting subscriber")
		if err := sub.Start(gctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("mqtt: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		return httpSrv.Shutdown(shutdownCtx)
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Print("energystore-v2: shutdown complete")
	return nil
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
