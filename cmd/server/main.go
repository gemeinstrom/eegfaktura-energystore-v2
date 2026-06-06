// Command energystore-v2 is the stateless time-series ingest+API service
// for the eegfaktura platform. See ../../README.md for the architectural
// rationale.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/api"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/config"
	mqttsub "github.com/gemeinstrom/eegfaktura-energystore-v2/internal/mqtt"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("energystore-v2: %v", err)
	}
}

func run() error {
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

	// MQTT handler: decode payload, transform into Slot batch, UPSERT.
	handler := func(_ context.Context, topic string, payload []byte) error {
		// TODO: decode MqttEnergyMessage and call st.UpsertSlots.
		_ = topic
		_ = payload
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
