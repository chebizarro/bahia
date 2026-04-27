// Package cashu provides a Cashu ecash wallet client for Loom payments.
package cashu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Wallet provides Cashu ecash payment capabilities.
type Wallet struct {
	mints      []MintConfig
	httpClient *http.Client
	logger     *zap.Logger

	mu       sync.RWMutex
	balances map[string]int64 // mintURL -> balance in sats
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
	return &Wallet{
		mints: mints,
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
	w.mu.Lock()
	defer w.mu.Unlock()

	// Check balance
	if w.balances[mintURL] < amount {
		return "", fmt.Errorf("insufficient balance: have %d, need %d", w.balances[mintURL], amount)
	}

	// Select proofs to spend
	proofs, remainder, err := w.selectProofs(mintURL, amount)
	if err != nil {
		return "", err
	}

	// In a real implementation, we would:
	// 1. Create a swap request with the mint
	// 2. Lock the new proofs to the recipient's pubkey (NUT-10 P2PK)
	// 3. Return the encoded token
	
	// For now, create a simplified token structure
	token := Token{
		Mint:   mintURL,
		Proofs: proofs,
		Unit:   "sat",
		Memo:   fmt.Sprintf("bahia-payment-%s", recipientPubkey[:8]),
	}

	// Encode token
	tokenBytes, err := json.Marshal(token)
	if err != nil {
		return "", err
	}

	// Update balance
	w.balances[mintURL] -= amount

	// If there's a remainder, we'd need to do a swap to get change
	if remainder > 0 {
		// In production, swap for change
		w.logger.Debug("payment created with remainder",
			zap.Int64("remainder", remainder),
		)
	}

	// Return base64-encoded token (simplified - real cashu uses cashuA prefix)
	encoded := "cashuA" + hex.EncodeToString(tokenBytes)
	return encoded, nil
}

// selectProofs selects proofs to cover the requested amount.
// Returns selected proofs and the remainder (excess amount).
func (w *Wallet) selectProofs(mintURL string, amount int64) ([]Proof, int64, error) {
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
	// Decode token
	if len(tokenString) < 6 || tokenString[:6] != "cashuA" {
		return 0, "", fmt.Errorf("invalid token format")
	}

	tokenBytes, err := hex.DecodeString(tokenString[6:])
	if err != nil {
		return 0, "", fmt.Errorf("decoding token: %w", err)
	}

	var token Token
	if err := json.Unmarshal(tokenBytes, &token); err != nil {
		return 0, "", fmt.Errorf("parsing token: %w", err)
	}

	// In production, we would swap the proofs with the mint to verify they're valid
	// and prevent double-spend
	
	w.mu.Lock()
	defer w.mu.Unlock()

	// Calculate amount
	var amount int64
	for _, p := range token.Proofs {
		amount += p.Amount
	}

	// Add proofs to wallet
	w.proofs[token.Mint] = append(w.proofs[token.Mint], token.Proofs...)
	w.balances[token.Mint] += amount

	w.logger.Info("received cashu token",
		zap.String("mint", token.Mint),
		zap.Int64("amount", amount),
	)

	return amount, token.Mint, nil
}

// AddProofs adds proofs to the wallet (e.g., from a mint quote).
func (w *Wallet) AddProofs(mintURL string, proofs []Proof) {
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
	// In production, POST to /v1/mint/quote/bolt11
	// For now, return a placeholder
	return &MintQuote{
		QuoteID:    generateID(),
		Request:    "lnbc" + fmt.Sprintf("%d", amount) + "...", // Placeholder invoice
		Amount:     amount,
		Expiry:     time.Now().Add(10 * time.Minute).Unix(),
		State:      "UNPAID",
	}, nil
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
	for _, m := range w.mints {
		if m.URL == mintURL {
			return true
		}
	}
	return false
}

// generateID creates a random ID.
func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
