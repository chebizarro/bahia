package nostr

import (
	"context"
	"log/slog"
	"sort"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewSlogZapLogger returns a zap logger, as RelayPool takes, that writes
// through logger (slog.Default when nil), so a component that logs with slog
// does not hand the pool a no-op logger and lose its relay diagnostics: AUTH
// failures, CLOSED refusals, retry-budget give-ups, reconnects.
func NewSlogZapLogger(logger *slog.Logger) *zap.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return zap.New(&slogZapCore{logger: logger})
}

// slogZapCore is a zapcore.Core that forwards entries to a slog.Logger.
type slogZapCore struct {
	logger *slog.Logger
	attrs  []any
}

func (c *slogZapCore) Enabled(level zapcore.Level) bool {
	return c.logger.Enabled(context.Background(), slogLevelFromZap(level))
}

func (c *slogZapCore) With(fields []zapcore.Field) zapcore.Core {
	return &slogZapCore{logger: c.logger, attrs: append(append([]any(nil), c.attrs...), slogArgsFromZap(fields)...)}
}

func (c *slogZapCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *slogZapCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	args := make([]any, 0, 2+len(c.attrs)+2*len(fields))
	if entry.LoggerName != "" {
		args = append(args, "logger", entry.LoggerName)
	}
	args = append(args, c.attrs...)
	args = append(args, slogArgsFromZap(fields)...)
	c.logger.Log(context.Background(), slogLevelFromZap(entry.Level), entry.Message, args...)
	return nil
}

func (c *slogZapCore) Sync() error { return nil }

func slogLevelFromZap(level zapcore.Level) slog.Level {
	switch {
	case level >= zapcore.ErrorLevel:
		return slog.LevelError
	case level == zapcore.WarnLevel:
		return slog.LevelWarn
	case level == zapcore.InfoLevel:
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}

// slogArgsFromZap renders zap fields as slog key/value arguments, in key
// order.
func slogArgsFromZap(fields []zapcore.Field) []any {
	if len(fields) == 0 {
		return nil
	}
	encoder := zapcore.NewMapObjectEncoder()
	for _, field := range fields {
		field.AddTo(encoder)
	}
	keys := make([]string, 0, len(encoder.Fields))
	for key := range encoder.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]any, 0, 2*len(keys))
	for _, key := range keys {
		args = append(args, key, encoder.Fields[key])
	}
	return args
}
