package soulfactory

import (
	"context"
	"log/slog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// newSlogZapLogger adapts SoulFactory's slog logger for the relay pool, which
// logs through zap.
func newSlogZapLogger(logger *slog.Logger) *zap.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return zap.New(&slogZapCore{logger: logger})
}

type slogZapCore struct {
	logger *slog.Logger
	attrs  []any
}

func (c *slogZapCore) Enabled(level zapcore.Level) bool {
	return c.logger.Enabled(context.Background(), slogLevel(level))
}

func (c *slogZapCore) With(fields []zapcore.Field) zapcore.Core {
	return &slogZapCore{logger: c.logger, attrs: append(append([]any(nil), c.attrs...), slogAttrs(fields)...)}
}

func (c *slogZapCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *slogZapCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	args := append(append([]any(nil), c.attrs...), slogAttrs(fields)...)
	c.logger.Log(context.Background(), slogLevel(entry.Level), entry.Message, args...)
	return nil
}

func (c *slogZapCore) Sync() error { return nil }

func slogLevel(level zapcore.Level) slog.Level {
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

func slogAttrs(fields []zapcore.Field) []any {
	if len(fields) == 0 {
		return nil
	}
	encoder := zapcore.NewMapObjectEncoder()
	for _, field := range fields {
		field.AddTo(encoder)
	}
	attrs := make([]any, 0, 2*len(encoder.Fields))
	for key, value := range encoder.Fields {
		attrs = append(attrs, key, value)
	}
	return attrs
}
