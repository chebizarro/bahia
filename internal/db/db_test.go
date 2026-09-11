package db

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

func TestConnectRedactsCredentialRepresentationsFromParseErrors(t *testing.T) {
	const secret = `DB-SECRET-SENTINEL:/@?&%\"<>`
	cfg := config.DBConfig{
		Host: "db.internal", Port: 5432, User: "bahia", Password: secret,
		Name: "bahia", SSLMode: "require",
	}
	originalParse := parsePoolConfig
	parsePoolConfig = func(dsn string) (*pgxpool.Config, error) {
		return nil, fmt.Errorf(
			"invalid database endpoint raw=%s query=%s path=%s userinfo=%s html=%s base64=%s dsn=%s",
			secret,
			url.QueryEscape(secret),
			url.PathEscape(secret),
			strings.TrimPrefix(url.UserPassword("_", secret).String(), "_:"),
			html.EscapeString(secret),
			base64.StdEncoding.EncodeToString([]byte(secret)),
			dsn,
		)
	}
	t.Cleanup(func() { parsePoolConfig = originalParse })

	_, err := Connect(context.Background(), cfg, zap.NewNop())
	if err == nil {
		t.Fatal("Connect() error = nil, want parse failure")
	}
	message := err.Error()
	for _, variant := range []string{
		secret,
		url.QueryEscape(secret),
		url.PathEscape(secret),
		strings.TrimPrefix(url.UserPassword("_", secret).String(), "_:"),
		html.EscapeString(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)),
	} {
		if strings.Contains(message, variant) {
			t.Fatalf("Connect() error leaked credential representation %q: %s", variant, message)
		}
	}
	if !strings.Contains(message, "invalid database endpoint") || !strings.Contains(message, "db.internal") {
		t.Fatalf("Connect() error lost useful diagnostics: %s", message)
	}
}
