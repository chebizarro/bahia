package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/soulfactory"
)

func main() {
	scenarioPath := flag.String("scenario", "", "path to sanitized runtime-validation scenario JSON")
	relaysCSV := flag.String("relays", "", "comma-separated canonical relay set")
	timeout := flag.Duration("timeout", 45*time.Second, "maximum EOSE-backed validation duration")
	flag.Parse()
	if strings.TrimSpace(*scenarioPath) == "" || strings.TrimSpace(*relaysCSV) == "" {
		fmt.Fprintln(os.Stderr, "usage: soulfactory-runtime-validate -scenario <path> -relays <wss://...,...>")
		os.Exit(2)
	}
	data, err := os.ReadFile(*scenarioPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("read scenario: %w", err))
		os.Exit(1)
	}
	var scenario soulfactory.RuntimeValidationScenario
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("parse scenario: %w", err))
		os.Exit(1)
	}
	relays := strings.Split(*relaysCSV, ",")
	source, err := soulfactory.NewRelayRuntimeValidationEventSource(relays)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer source.Close()
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(base, *timeout)
	defer cancel()
	report, validationErr := soulfactory.ValidateRuntimeScenario(ctx, source, scenario)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if validationErr != nil {
		fmt.Fprintln(os.Stderr, validationErr)
		os.Exit(1)
	}
}
