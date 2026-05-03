// Package cashu provides a Cashu ecash wallet client for Loom payments.
package cashu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ErrNotConfigured is returned when wallet operations are requested without configured mints.
var ErrNotConfigured = errors.New("cashu wallet not configured")

// ErrMintNotConfigured is returned when an operation targets a mint the wallet does not trust.
var ErrMintNotConfigured = errors.New("cashu mint not configured")

// ErrMintBackedFlowUnsupported is returned for paths that would otherwise create or accept fake ecash.
var ErrMintBackedFlowUnsupported = errors.New("cashu mint-backed proof flow not implemented")

// Wallet provides Cashu ecash payment capabilities.
type Wallet struct {
	mints      []MintConfig
	httpClient *http.Client
	logger     *zap.Logger

	mu       sync.RWMutex
	balances map[string]int64   // mintURL -> balance in sats
	proofs   map[string][]Proof // mintURL -> available proofs
}

// MintConfig configures a Cashu mint.
type MintConfig struct {
	URL           string `json:"url"`
	PayoutEnabled bool   `json:"payout_enabled"`
}

// Proof represents a Cashu proof (simplified).
type Proof struct {
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
	Secret string `json:"secret"`
	C      string `json:"C"`
}

// Token represents an encoded Cashu token.
type Token struct {
	Mint   string  `json:"mint"`
	Proofs []Proof `json:"proofs"`
	Unit   string  `json:"unit,omitempty"`
	Memo   string  `json:"memo,omitempty"`
}

// NewWallet creates a new Cashu wallet.
func NewWallet(mints []MintConfig, logger *zap.Logger) *Wallet {
	if logger == nil {
		logger = zap.NewNop()
	}
	cleanMints := make([]MintConfig, 0, len(mints))
	for _, mint := range mints {
		mint.URL = normalizeMintURL(mint.URL)
		if mint.URL == "" {
			continue
		}
		cleanMints = append(cleanMints, mint)
	}
	return &Wallet{
		mints: cleanMints,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:   logger,
		balances: make(map[string]int64),
		proofs:   make(map[string][]Proof),
	}
}

// Initialize connects to configured mints and fetches initial state.
func (w *Wallet) Initialize(ctx context.Context) error {
	if len(w.mints) == 0 {
		return ErrNotConfigured
	}
	for _, mint := range w.mints {
		info, err := w.getMintInfo(ctx, mint.URL)
		if err != nil {
			w.logger.Warn("failed to connect to mint",
				zap.String("url", mint.URL),
				zap.Error(err),
			)
			continue
		}
		w.logger.Info("connected to cashu mint",
			zap.String("url", mint.URL),
			zap.String("name", info.Name),
		)
	}
	return nil
}

// MintInfo contains information about a Cashu mint.
type MintInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// getMintInfo fetches mint information.
func (w *Wallet) getMintInfo(ctx context.Context, mintURL string) (*MintInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", mintURL+"/v1/info", nil)
	if err != nil {
		return nil, err
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mint returned %d: %s", resp.StatusCode, string(body))
	}

	var info MintInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// GetBalance returns the wallet balance for a specific mint.
func (w *Wallet) GetBalance(mintURL string) int64 {
	mintURL = normalizeMintURL(mintURL)
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.balances[mintURL]
}

// GetAllBalances returns balances for all configured mints.
func (w *Wallet) GetAllBalances() map[string]int64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := make(map[string]int64)
	for k, v := range w.balances {
		result[k] = v
	}
	return result
}

// CreatePaymentToken creates a Cashu token for the specified amount.
// The token is locked to the recipient's pubkey for NUT-10 P2PK.
func (w *Wallet) CreatePaymentToken(ctx context.Context, mintURL string, amount int64, recipientPubkey string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	mintURL = normalizeMintURL(mintURL)
	if !w.HasMint(mintURL) {
		return "", fmt.Errorf("%w: %s", ErrMintNotConfigured, mintURL)
	}
	if amount <= 0 {
		return "", fmt.Errorf("amount must be > 0")
	}
	if strings.TrimSpace(recipientPubkey) == "" {
		return "", fmt.Errorf("recipient pubkey is required")
	}

	w.mu.RLock()
	balance := w.balances[mintURL]
	w.mu.RUnlock()
	if balance < amount {
		return "", fmt.Errorf("insufficient balance: have %d, need %d", balance, amount)
	}

	return "", ErrMintBackedFlowUnsupported
}

// selectProofs selects proofs to cover the requested amount.
// Returns selected proofs and the remainder (excess amount).
func (w *Wallet) selectProofs(mintURL string, amount int64) ([]Proof, int64, error) {
	mintURL = normalizeMintURL(mintURL)
	available := w.proofs[mintURL]
	if len(available) == 0 {
		return nil, 0, fmt.Errorf("no proofs available for mint %s", mintURL)
	}

	var selected []Proof
	var total int64
	var remaining []Proof

	for _, p := range available {
		if total < amount {
			selected = append(selected, p)
			total += p.Amount
		} else {
			remaining = append(remaining, p)
		}
	}

	if total < amount {
		return nil, 0, fmt.Errorf("insufficient proofs: have %d, need %d", total, amount)
	}

	// Update proofs
	w.proofs[mintURL] = remaining

	return selected, total - amount, nil
}

// ReceiveToken redeems a Cashu token and adds the proofs to the wallet.
func (w *Wallet) ReceiveToken(ctx context.Context, tokenString string) (int64, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	if strings.TrimSpace(tokenString) == "" {
		return 0, "", fmt.Errorf("token is required")
	}
	return 0, "", ErrMintBackedFlowUnsupported
}

// AddProofs adds proofs to the wallet (e.g., from a mint quote).
func (w *Wallet) AddProofs(mintURL string, proofs []Proof) {
	mintURL = normalizeMintURL(mintURL)
	w.mu.Lock()
	defer w.mu.Unlock()

	var total int64
	for _, p := range proofs {
		total += p.Amount
	}

	w.proofs[mintURL] = append(w.proofs[mintURL], proofs...)
	w.balances[mintURL] += total
}

// CreateMintQuote requests a Lightning invoice from the mint for funding.
func (w *Wallet) CreateMintQuote(ctx context.Context, mintURL string, amount int64) (*MintQuote, error) {
	mintURL = normalizeMintURL(mintURL)
	if !w.HasMint(mintURL) {
		return nil, fmt.Errorf("%w: %s", ErrMintNotConfigured, mintURL)
	}
	if amount <= 0 {
		return nil, fmt.Errorf("amount must be > 0")
	}

	body := map[string]interface{}{
		"amount": amount,
		"unit":   "sat",
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal mint quote request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", mintURL+"/v1/mint/quote/bolt11", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create mint quote request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mint quote request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read mint quote response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mint quote API error %d: %s", resp.StatusCode, string(respBody))
	}

	var quote MintQuote
	if err := json.Unmarshal(respBody, &quote); err != nil {
		return nil, fmt.Errorf("decode mint quote response: %w", err)
	}
	if quote.QuoteID == "" || quote.Request == "" {
		return nil, fmt.Errorf("mint quote response missing quote or request")
	}
	if quote.Amount == 0 {
		quote.Amount = amount
	}
	return &quote, nil
}

// MintQuote represents a mint quote for funding the wallet.
type MintQuote struct {
	QuoteID string `json:"quote"`
	Request string `json:"request"` // Lightning invoice
	Amount  int64  `json:"amount"`
	Expiry  int64  `json:"expiry"`
	State   string `json:"state"` // UNPAID, PAID, ISSUED
}

// EstimateCost calculates the cost for a job based on worker pricing.
func (w *Wallet) EstimateCost(pricePerSecond int, estimatedDurationSecs int, minDuration int) int64 {
	duration := estimatedDurationSecs
	if duration < minDuration {
		duration = minDuration
	}
	return int64(pricePerSecond * duration)
}

// GetConfiguredMints returns the list of configured mints.
func (w *Wallet) GetConfiguredMints() []MintConfig {
	return w.mints
}

// HasMint checks if a mint is configured.
func (w *Wallet) HasMint(mintURL string) bool {
	mintURL = normalizeMintURL(mintURL)
	for _, m := range w.mints {
		if m.URL == mintURL {
			return true
		}
	}
	return false
}

func normalizeMintURL(mintURL string) string {
	return strings.TrimRight(strings.TrimSpace(mintURL), "/")
}
