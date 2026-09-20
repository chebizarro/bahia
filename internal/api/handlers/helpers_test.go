package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type failWriter struct {
	*httptest.ResponseRecorder
}

func (f *failWriter) Write(buf []byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestWriteJSONLogsEncodeError(t *testing.T) {
	core, observed := observer.New(zapcore.ErrorLevel)
	undo := zap.ReplaceGlobals(zap.New(core))
	defer undo()

	fw := &failWriter{httptest.NewRecorder()}
	writeJSON(fw, http.StatusOK, map[string]any{"key": "value"})

	logs := observed.FilterMessage("failed to encode JSON response").All()
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry for encode failure, got %d", len(logs))
	}
}
