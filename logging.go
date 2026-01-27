package main

import (
	"log/slog"
	"os"
	"strings"
)

// initLogging configures the default slog logger based on HELPDESK_LOG_LEVEL
// env var and an optional -log-level / --log-level CLI flag (flag wins).
// It returns args with the flag stripped so downstream flag parsers (e.g. the
// ADK launcher) don't choke on it.
func initLogging(args []string) []string {
	levelStr := os.Getenv("HELPDESK_LOG_LEVEL")
	if levelStr == "" {
		levelStr = "info"
	}

	// Scan args for -log-level / --log-level, strip it from the slice.
	var remaining []string
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// --log-level=value
		if strings.HasPrefix(arg, "--log-level=") {
			levelStr = strings.TrimPrefix(arg, "--log-level=")
			continue
		}
		if strings.HasPrefix(arg, "-log-level=") {
			levelStr = strings.TrimPrefix(arg, "-log-level=")
			continue
		}

		// -log-level value / --log-level value
		if arg == "-log-level" || arg == "--log-level" {
			if i+1 < len(args) {
				levelStr = args[i+1]
				i++ // skip the value
			}
			continue
		}

		remaining = append(remaining, arg)
	}

	var level slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default: // "info" or anything unrecognised
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	return remaining
}
