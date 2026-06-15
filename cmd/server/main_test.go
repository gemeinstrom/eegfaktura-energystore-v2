package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/decode"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/metrics"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

func TestTenantFromTopic(t *testing.T) {
	cases := map[string]string{
		// Pilot/v1-inverter shape: tenant verbatim from position [1].
		"eegfaktura/vfeeg/energy/TE100200":        "vfeeg",
		"eegfaktura/vfeeg/energy/TE100200/extras": "vfeeg",
		"a/b":                                     "b",
		// Prod-EDA-energy shape: tenant from position [3], upper-cased.
		// eegfaktura-eda-comm publishes as
		// `${energyTopic}/${receiver.toLowerCase}` with energyTopic =
		// "eda/response/energy" → CC100153 case below.
		"eda/response/energy/cc100153":         "CC100153",
		"eda/response/energy/te100200":         "TE100200",
		"eda/response/energy/cc100153/garbage": "CC100153",
		// Degenerate inputs.
		"eegfaktura/": "",
		"":            "",
		"single":      "",
	}
	for in, want := range cases {
		if got := tenantFromTopic(in); got != want {
			t.Errorf("tenantFromTopic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCorsOriginsFromEnv(t *testing.T) {
	t.Setenv("ESV2_CORS_ALLOWED_ORIGINS", "")
	if got := corsOriginsFromEnv(); len(got) != 1 || got[0] != "*" {
		t.Fatalf("default: %v", got)
	}
	t.Setenv("ESV2_CORS_ALLOWED_ORIGINS", "https://app.example.org , https://admin.example.org")
	got := corsOriginsFromEnv()
	if len(got) != 2 || got[0] != "https://app.example.org" || got[1] != "https://admin.example.org" {
		t.Fatalf("parsed: %v", got)
	}
}

func TestIngestHandlerEnergy_OK(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	st := store.FromPool(mock)
	mtr := metrics.New()
	logger := slog.Default()

	exp := mock.ExpectBatch()
	exp.ExpectExec(`INSERT INTO energy_data`).
		WithArgs("vfeeg", "TE100200", "AT00100", "1-1:1.9.0 G.01",
			pgxmock.AnyArg(), float64(0.118), int16(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	h := makeIngestHandler(st, mtr, logger, "energy", "", decode.DecryptConfig{})

	payload := []byte(`{"message":{"meter":{"meteringPoint":"AT00100"},"ecId":"TE100200","energy":{"data":[{"meterCode":"1-1:1.9.0 G.01","value":[{"from":1667948400000,"to":1667949300000,"method":"L1","value":0.118}]}]}}}`)
	if err := h(context.Background(), "eegfaktura/vfeeg/energy/TE100200", payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestIngestHandlerInverter_OverridesECID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	st := store.FromPool(mock)
	mtr := metrics.New()

	// The payload says ecId=TE100200; the inverter handler must overwrite
	// it to "inverter" before upsert (mirrors v1 behaviour).
	exp := mock.ExpectBatch()
	exp.ExpectExec(`INSERT INTO energy_data`).
		WithArgs("vfeeg", "inverter", "AT00100", "1-1:1.9.0 G.01",
			pgxmock.AnyArg(), float64(0.5), int16(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	h := makeIngestHandler(st, mtr, slog.Default(), "inverter", "inverter", decode.DecryptConfig{})

	payload := []byte(`{"message":{"meter":{"meteringPoint":"AT00100"},"ecId":"TE100200","energy":{"data":[{"meterCode":"1-1:1.9.0 G.01","value":[{"from":1667948400000,"to":1667949300000,"method":"L1","value":0.5}]}]}}}`)
	if err := h(context.Background(), "eegfaktura/vfeeg/energy/TE100200", payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestIngestHandler_DecodeFailureToDLQ(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	st := store.FromPool(mock)
	mtr := metrics.New()

	mock.ExpectExec(`INSERT INTO mqtt_dlq`).
		WithArgs("eegfaktura/vfeeg/energy/TE100200", "decode", pgxmock.AnyArg(), []byte("{garbage")).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	h := makeIngestHandler(st, mtr, slog.Default(), "energy", "", decode.DecryptConfig{})
	if err := h(context.Background(), "eegfaktura/vfeeg/energy/TE100200", []byte("{garbage")); err == nil {
		t.Fatal("expected decode error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
