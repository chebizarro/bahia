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
		logger: logger,
	}
}

// Initialize connects to configured mints and fetches initial state.
func (w *Wallet) Initialize(context.Context) error {
	if len(w.mints) == 0 {
		return ErrNotConfigured
	}
	return fmt.Errorf("%w: balance, send, receive, swap, and proof verification are unavailable", ErrMintBackedFlowUnsupported)
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

// GetBalance fails explicitly because authoritative mint-backed proof state is unavailable.
func (w *Wallet) GetBalance(mintURL string) (int64, error) {
	mintURL = normalizeMintURL(mintURL)
	if !w.HasMint(mintURL) {
		return 0, fmt.Errorf("%w: %s", ErrMintNotConfigured, mintURL)
	}
	return 0, ErrMintBackedFlowUnsupported
}

// GetAllBalances fails explicitly because this adapter has no persistent,
// mint-verified proof store.
func (w *Wallet) GetAllBalances() (map[string]int64, error) {
	return nil, ErrMintBackedFlowUnsupported
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

	return "", ErrMintBackedFlowUnsupported
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

// WalletCapabilities reports only capabilities implemented end-to-end.
type WalletCapabilities struct {
	MintInfo          bool   `json:"mint_info"`
	MintQuote         bool   `json:"mint_quote"`
	Balance           bool   `json:"balance"`
	CreatePayment     bool   `json:"create_payment"`
	ReceivePayment    bool   `json:"receive_payment"`
	ProofVerification bool   `json:"proof_verification"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// Capabilities returns an honest description of this adapter's readiness.
func (w *Wallet) Capabilities() WalletCapabilities {
	return WalletCapabilities{
		MintInfo:          true,
		MintQuote:         true,
		Balance:           false,
		CreatePayment:     false,
		ReceivePayment:    false,
		ProofVerification: false,
		UnavailableReason: ErrMintBackedFlowUnsupported.Error(),
	}
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
