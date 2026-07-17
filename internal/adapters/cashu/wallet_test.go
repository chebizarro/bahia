package cashu

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestWalletReportsUnavailableMintBackedCapabilities(t *testing.T) {
	wallet := NewWallet([]MintConfig{{URL: "https://mint.example.com"}}, zap.NewNop())
	caps := wallet.Capabilities()
	if !caps.MintInfo || !caps.MintQuote {
		t.Fatalf("implemented read/quote capabilities not reported: %#v", caps)
	}
	if caps.Balance || caps.CreatePayment || caps.ReceivePayment || caps.ProofVerification {
		t.Fatalf("unsupported payment capabilities reported as available: %#v", caps)
	}
	if caps.UnavailableReason == "" {
		t.Fatal("unsupported capability reason is empty")
	}
	if err := wallet.Initialize(t.Context()); !errors.Is(err, ErrMintBackedFlowUnsupported) {
		t.Fatalf("Initialize() error = %v, want ErrMintBackedFlowUnsupported", err)
	}
}

func TestWalletBalanceAndPaymentOperationsFailClosed(t *testing.T) {
	mintURL := "https://mint.example.com"
	wallet := NewWallet([]MintConfig{{URL: mintURL}}, zap.NewNop())

	if balance, err := wallet.GetBalance(mintURL); balance != 0 || !errors.Is(err, ErrMintBackedFlowUnsupported) {
		t.Fatalf("GetBalance() = (%d, %v), want unsupported", balance, err)
	}
	if balances, err := wallet.GetAllBalances(); balances != nil || !errors.Is(err, ErrMintBackedFlowUnsupported) {
		t.Fatalf("GetAllBalances() = (%#v, %v), want unsupported", balances, err)
	}
	if token, err := wallet.CreatePaymentToken(t.Context(), mintURL, 50, "recipientpubkey123"); token != "" || !errors.Is(err, ErrMintBackedFlowUnsupported) {
		t.Fatalf("CreatePaymentToken() = (%q, %v), want unsupported", token, err)
	}
	if amount, mint, err := wallet.ReceiveToken(t.Context(), "cashuAdeadbeef"); amount != 0 || mint != "" || !errors.Is(err, ErrMintBackedFlowUnsupported) {
		t.Fatalf("ReceiveToken() = (%d, %q, %v), want unsupported", amount, mint, err)
	}
}

func TestWalletRejectsInvalidPaymentInputsBeforeCapabilityError(t *testing.T) {
	mintURL := "https://mint.example.com"
	wallet := NewWallet([]MintConfig{{URL: mintURL}}, zap.NewNop())
	if _, err := wallet.CreatePaymentToken(t.Context(), "https://other.example.com", 1, "recipient"); !errors.Is(err, ErrMintNotConfigured) {
		t.Fatalf("unconfigured mint error = %v", err)
	}
	if _, err := wallet.CreatePaymentToken(t.Context(), mintURL, 0, "recipient"); err == nil {
		t.Fatal("zero amount should fail")
	}
	if _, err := wallet.CreatePaymentToken(t.Context(), mintURL, 1, ""); err == nil {
		t.Fatal("empty recipient should fail")
	}
	if _, _, err := wallet.ReceiveToken(t.Context(), ""); err == nil {
		t.Fatal("empty token should fail")
	}
}

func TestWalletCreateMintQuoteCallsMint(t *testing.T) {
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
			"quote": "quote-123", "request": "lnbc123", "amount": req.Amount, "state": "UNPAID",
		})
	}))
	defer server.Close()

	wallet := NewWallet([]MintConfig{{URL: server.URL}}, zap.NewNop())
	quote, err := wallet.CreateMintQuote(t.Context(), server.URL+"/", 123)
	if err != nil {
		t.Fatalf("CreateMintQuote() error = %v", err)
	}
	if gotPath != "/v1/mint/quote/bolt11" || gotAmount != 123 || quote.QuoteID != "quote-123" || quote.Request != "lnbc123" {
		t.Fatalf("quote = %+v path=%s amount=%d", quote, gotPath, gotAmount)
	}
}

func TestWalletConfigurationAndEstimateCost(t *testing.T) {
	wallet := NewWallet([]MintConfig{{URL: "https://mint.example.com/", PayoutEnabled: true}}, zap.NewNop())
	if !wallet.HasMint("https://mint.example.com") || len(wallet.GetConfiguredMints()) != 1 {
		t.Fatalf("mint normalization/configuration failed: %#v", wallet.GetConfiguredMints())
	}
	if got := wallet.EstimateCost(10, 5, 10); got != 100 {
		t.Fatalf("EstimateCost() = %d, want 100", got)
	}
}

func TestWalletInitializeNoMints(t *testing.T) {
	wallet := NewWallet(nil, zap.NewNop())
	if err := wallet.Initialize(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Initialize() error = %v, want ErrNotConfigured", err)
	}
}
