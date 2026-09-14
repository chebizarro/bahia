package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/openagentsinc/bahia/internal/soulfactory"
)

const (
	exitInvalid   = 1
	exitUsage     = 2
	exitAmbiguous = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("soulfactory-legacy-adoption-report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "path to sanitized read-only inventory JSON, or - for stdin")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*inputPath) == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: soulfactory-legacy-adoption-report -input <path|->")
		return exitUsage
	}

	reader := stdin
	var closeInput func() error
	if *inputPath != "-" {
		file, err := os.Open(*inputPath)
		if err != nil {
			fmt.Fprintln(stderr, fmt.Errorf("open input: %w", err))
			return exitInvalid
		}
		reader = file
		closeInput = file.Close
	}
	if closeInput != nil {
		defer func() { _ = closeInput() }()
	}

	input, err := decodeInput(reader)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitInvalid
	}
	report, classificationErr := soulfactory.ClassifyLegacyAgents(input)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, fmt.Errorf("write report: %w", err))
		return exitInvalid
	}
	if errors.Is(classificationErr, soulfactory.ErrLegacyAdoptionAmbiguous) {
		fmt.Fprintln(stderr, "refused: legacy agent adoption classification is ambiguous")
		return exitAmbiguous
	}
	if classificationErr != nil {
		fmt.Fprintln(stderr, classificationErr)
		return exitInvalid
	}
	return 0
}

func decodeInput(reader io.Reader) (soulfactory.LegacyAdoptionInput, error) {
	var input soulfactory.LegacyAdoptionInput
	data, err := io.ReadAll(reader)
	if err != nil {
		return input, fmt.Errorf("read input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("parse input: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return input, errors.New("parse input: multiple JSON values")
		}
		return input, fmt.Errorf("parse input trailing data: %w", err)
	}
	return input, nil
}
