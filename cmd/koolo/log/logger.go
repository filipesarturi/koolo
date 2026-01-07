package log

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

var logFileHandler *os.File

func FlushLog() {
	if logFileHandler != nil {
		logFileHandler.Sync()
	}
}

func FlushAndClose() error {
	if logFileHandler != nil {
		logFileHandler.Sync()
		return logFileHandler.Close()
	}

	return nil
}

// NewLogger creates a new logger with the specified log level
// logLevel can be "debug", "info", "warn", or "error"
// If logLevel is empty, it falls back to the debug bool parameter for backward compatibility
func NewLogger(debug bool, logDir, supervisor string) (*slog.Logger, error) {
	return NewLoggerWithLevel("", debug, logDir, supervisor)
}

// NewLoggerWithLevel creates a new logger with a specific log level
// logLevel can be "debug", "info", "warn", or "error"
// If logLevel is empty, it falls back to the debug bool parameter
func NewLoggerWithLevel(logLevel string, debug bool, logDir, supervisor string) (*slog.Logger, error) {
	if logDir == "" {
		logDir = "logs"
	}

	if _, err := os.Stat(logDir); errors.Is(err, os.ErrNotExist) {
		err := os.MkdirAll(logDir, os.ModePerm)
		if err != nil {
			return nil, fmt.Errorf("error creating log directory: %w", err)
		}
	}

	fileName := "Koolo-log-" + time.Now().Format("2006-01-02-15-04-05") + ".txt"
	if supervisor != "" {
		fileName = fmt.Sprintf("Supervisor-log-%s-%s.txt", supervisor, time.Now().Format("2006-01-02-15-04-05"))
	}

	lfh, err := os.Create(logDir + "/" + fileName)
	if err != nil {
		return nil, err
	}
	logFileHandler = lfh

	var level slog.Level
	if logLevel != "" {
		switch strings.ToLower(logLevel) {
		case "debug":
			level = slog.LevelDebug
		case "info":
			level = slog.LevelInfo
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		default:
			// Invalid level, fall back to debug bool
			if debug {
				level = slog.LevelDebug
			} else {
				level = slog.LevelInfo
			}
		}
	} else {
		// Backward compatibility: use debug bool
		if debug {
			level = slog.LevelDebug
		} else {
			level = slog.LevelInfo
		}
	}

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key != slog.TimeKey {
				return a
			}

			t := a.Value.Time()
			a.Value = slog.StringValue(t.Format(time.TimeOnly))

			return a
		},
	}
	handler := slog.NewTextHandler(io.MultiWriter(logFileHandler, os.Stdout), opts)

	return slog.New(handler), nil
}
