package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestMalformedDSNCannotLeakPasswordThroughErrorOrLog(t *testing.T) {
	const password = "DB-password+with:/special?characters"
	cfg := config.DBConfig{Host: "db.internal", Port: 5432, User: "bahia", Password: password, Name: "bahia", SSLMode: "invalid-mode"}
	_, err := Connect(context.Background(), cfg, zap.NewNop())
	if err == nil {
		t.Fatal("invalid sslmode must cause a real pgx parse error")
	}
	var logs bytes.Buffer
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&logs), zap.DebugLevel))
	logger.Warn("postgres cache unavailable", zap.Error(cfg.RedactError(err)))
	outputs := []string{logs.String()}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		outputs = append(outputs, cause.Error(), fmt.Sprintf("%+v", cause), fmt.Sprintf("%#v", cause))
	}
	for _, output := range outputs {
		for _, secret := range []string{password, url.QueryEscape(password), url.PathEscape(password), strings.TrimPrefix(url.UserPassword("_", password).String(), "_:")} {
			if strings.Contains(output, secret) {
				t.Fatal("database parse error or structured log leaked the password")
			}
		}
	}
	if !strings.Contains(err.Error(), "invalid-mode") || !strings.Contains(err.Error(), "db.internal") {
		t.Fatalf("lost useful diagnostics: %v", err)
	}
}
