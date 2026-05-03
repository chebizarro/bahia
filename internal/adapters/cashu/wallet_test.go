package cashu

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestWallet_EstimateCost(t *testing.T) {
	wallet := NewWallet(nil, zap.NewNop())

	tests := []struct {
		name           string
		pricePerSecond int
		estimatedSecs  int
		minDuration    int
		want           int64
	}{
		{
			name:           "normal duration",
			pricePerSecond: 10,
			estimatedSecs:  60,
			minDuration:    10,
			want:           600,
		},
		{
			name:           "below minimum duration",
			pricePerSecond: 10,
			estimatedSecs:  5,
			minDuration:    10,
			want:           100, // uses minDuration
		},
		{
			name:           "exactly minimum duration",
			pricePerSecond: 10,
			estimatedSecs:  10,
			minDuration:    10,
			want:           100,
		},
		{
			name:           "zero price",
			pricePerSecond: 0,
			estimatedSecs:  60,
			minDuration:    10,
			want:           0,
		},
		{
			name:           "large duration",
			pricePerSecond: 5,
			estimatedSecs:  3600,
			minDuration:    10,
			want:           18000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wallet.EstimateCost(tt.pricePerSecond, tt.estimatedSecs, tt.minDuration)
			if got != tt.want {
				t.Errorf("EstimateCost() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWallet_GetBalance(t *testing.T) {
	wallet := NewWallet(nil, zap.NewNop())

	// Initially zero
	if got := wallet.GetBalance("https://mint.example.com"); got != 0 {
		t.Errorf("GetBalance() = %v, want 0", got)
	}

	// Add some proofs
	wallet.AddProofs("https://mint.example.com", []Proof{
		{ID: "1", Amount: 100},
		{ID: "2", Amount: 50},
	})

	if got := wallet.GetBalance("https://mint.example.com"); got != 150 {
		t.Errorf("GetBalance() = %v, want 150", got)
	}
}

func TestWallet_NormalizesMintMapKeys(t *testing.T) {
	wallet := NewWallet([]MintConfig{{URL: "https://mint.example.com/"}}, zap.NewNop())
	wallet.AddProofs("https://mint.example.com/", []Proof{{ID: "1", Amount: 10}})

	if got := wallet.GetBalance("https://mint.example.com"); got != 10 {
		t.Fatalf("GetBalance normalized mint = %d, want 10", got)
	}
	if _, _, err := wallet.selectProofs("https://mint.example.com/", 10); err != nil {
		t.Fatalf("selectProofs with normalized mint error = %v", err)
	}
}

func TestWallet_GetAllBalances(t *testing.T) {
	mints := []MintConfig{
		{URL: "https://mint1.example.com"},
		{URL: "https://mint2.example.com"},
	}
	wallet := NewWallet(mints, zap.NewNop())

	wallet.AddProofs("https://mint1.example.com", []Proof{{ID: "1", Amount: 100}})
	wallet.AddProofs("https://mint2.example.com", []Proof{{ID: "2", Amount: 200}})

	balances := wallet.GetAllBalances()

	if balances["https://mint1.example.com"] != 100 {
		t.Errorf("mint1 balance = %v, want 100", balances["https://mint1.example.com"])
	}
	if balances["https://mint2.example.com"] != 200 {
		t.Errorf("mint2 balance = %v, want 200", balances["https://mint2.example.com"])
	}
}

func TestWallet_HasMint(t *testing.T) {
	mints := []MintConfig{
		{URL: "https://mint1.example.com"},
		{URL: "https://mint2.example.com"},
	}
	wallet := NewWallet(mints, zap.NewNop())

	if !wallet.HasMint("https://mint1.example.com") {
		t.Error("HasMint() = false for configured mint")
	}
	if !wallet.HasMint("https://mint2.example.com") {
		t.Error("HasMint() = false for configured mint")
	}
	if wallet.HasMint("https://unknown.example.com") {
		t.Error("HasMint() = true for unconfigured mint")
	}
}

func TestWallet_GetConfiguredMints(t *testing.T) {
	mints := []MintConfig{
		{URL: "https://mint1.example.com", PayoutEnabled: true},
		{URL: "https://mint2.example.com", PayoutEnabled: false},
	}
	wallet := NewWallet(mints, zap.NewNop())

	got := wallet.GetConfiguredMints()
	if len(got) != 2 {
		t.Errorf("GetConfiguredMints() returned %d mints, want 2", len(got))
	}
}

func TestWallet_SelectProofs(t *testing.T) {
	wallet := NewWallet(nil, zap.NewNop())
	mintURL := "https://mint.example.com"

	// Add proofs of various amounts
	wallet.AddProofs(mintURL, []Proof{
		{ID: "1", Amount: 10},
		{ID: "2", Amount: 20},
		{ID: "3", Amount: 50},
		{ID: "4", Amount: 100},
	})

	// Select enough for 75 sats
	proofs, remainder, err := wallet.selectProofs(mintURL, 75)
	if err != nil {
		t.Fatalf("selectProofs() error = %v", err)
	}

	// Should select proofs summing to >= 75
	var total int64
	for _, p := range proofs {
		total += p.Amount
	}
	if total < 75 {
		t.Errorf("selected proofs total %d, less than requested 75", total)
	}
	if total-remainder != 75 {
		t.Errorf("total - remainder = %d, want 75", total-remainder)
	}
}

func TestWallet_SelectProofs_InsufficientBalance(t *testing.T) {
	wallet := NewWallet(nil, zap.NewNop())
	mintURL := "https://mint.example.com"

	wallet.AddProofs(mintURL, []Proof{
		{ID: "1", Amount: 10},
	})

	_, _, err := wallet.selectProofs(mintURL, 100)
	if err == nil {
		t.Error("selectProofs() should error with insufficient balance")
	}
}

func TestWallet_SelectProofs_NoProofs(t *testing.T) {
	wallet := NewWallet(nil, zap.NewNop())

	_, _, err := wallet.selectProofs("https://unknown.example.com", 10)
	if err == nil {
		t.Error("selectProofs() should error with no proofs")
	}
}

func TestWallet_CreatePaymentToken_UnsupportedInsteadOfFakeToken(t *testing.T) {
	mintURL := "https://mint.example.com"
	wallet := NewWallet([]MintConfig{{URL: mintURL}}, zap.NewNop())
	wallet.AddProofs(mintURL, []Proof{
		{ID: "test1", Amount: 100, Secret: "secret1", C: "commitment1"},
	})

	ctx := t.Context()
	token, err := wallet.CreatePaymentToken(ctx, mintURL, 50, "recipientpubkey123")
	if !errors.Is(err, ErrMintBackedFlowUnsupported) {
		t.Fatalf("CreatePaymentToken() error = %v, want ErrMintBackedFlowUnsupported", err)
	}
	if token != "" {
		t.Fatalf("CreatePaymentToken() returned fake token %q", token)
	}
	if got := wallet.GetBalance(mintURL); got != 100 {
		t.Errorf("balance after unsupported payment = %d, want 100", got)
	}
}

func TestWallet_CreatePaymentToken_InsufficientBalance(t *testing.T) {
	mintURL := "https://mint.example.com"
	wallet := NewWallet([]MintConfig{{URL: mintURL}}, zap.NewNop())

	wallet.AddProofs(mintURL, []Proof{
		{ID: "1", Amount: 10},
	})

	ctx := t.Context()
	_, err := wallet.CreatePaymentToken(ctx, mintURL, 100, "recipient")
	if err == nil {
		t.Error("CreatePaymentToken() should error with insufficient balance")
	}
}

func TestWallet_ReceiveTokenUnsupported(t *testing.T) {
	wallet := NewWallet([]MintConfig{{URL: "https://mint.example.com"}}, zap.NewNop())
	_, _, err := wallet.ReceiveToken(t.Context(), "cashuAdeadbeef")
	if !errors.Is(err, ErrMintBackedFlowUnsupported) {
		t.Fatalf("ReceiveToken() error = %v, want ErrMintBackedFlowUnsupported", err)
	}
}

func TestWallet_CreateMintQuoteCallsMint(t *testing.T) {
	var gotPath string
	var gotAmount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var req struct {
			Amount int64  `json:"amount"`
			Unit   string `json:"unit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotAmount = req.Amount
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"quote":   "quote-123",
			"request": "lnbc123",
			"amount":  req.Amount,
			"state":   "UNPAID",
		})
	}))
	defer server.Close()

	wallet := NewWallet([]MintConfig{{URL: server.URL}}, zap.NewNop())
	quote, err := wallet.CreateMintQuote(t.Context(), server.URL+"/", 123)
	if err != nil {
		t.Fatalf("CreateMintQuote() error = %v", err)
	}
	if gotPath != "/v1/mint/quote/bolt11" {
		t.Fatalf("mint path = %s, want /v1/mint/quote/bolt11", gotPath)
	}
	if gotAmount != 123 || quote.QuoteID != "quote-123" || quote.Request != "lnbc123" {
		t.Fatalf("quote = %+v, gotAmount=%d", quote, gotAmount)
	}
}

func TestWallet_InitializeNoMints(t *testing.T) {
	wallet := NewWallet(nil, zap.NewNop())
	if err := wallet.Initialize(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Initialize() error = %v, want ErrNotConfigured", err)
	}
}
