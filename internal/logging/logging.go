// Package logging configures the stdlib slog handler used across
// energystore-v2. JSON for production, text for local dev.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Setup installs a default *slog.Logger as the package default and
// returns it. Format = "json" (default) or "text". Level = debug | info |
// warn | error. Reads ESV2_LOG_FORMAT / ESV2_LOG_LEVEL.
func Setup() *slog.Logger {
	format := strings.ToLower(os.Getenv("ESV2_LOG_FORMAT"))
	if format == "" {
		format = "json"
	}
	level := parseLevel(os.Getenv("ESV2_LOG_LEVEL"))

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	logger := slog.New(h).With(
		"service", "energystore-v2",
	)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
