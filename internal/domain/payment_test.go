package domain

import "testing"

func TestEstimateCost(t *testing.T) {
	pricing := WorkerPricing{
		MintURL:        "https://mint.example.com",
		PricePerSecond: 10,
		Unit:           "sat",
	}

	est := EstimateCost(pricing, 300) // 5 minutes
	if est.EstimatedCost != 3000 {
		t.Errorf("EstimatedCost = %d, want 3000", est.EstimatedCost)
	}
	if est.MintURL != "https://mint.example.com" {
		t.Errorf("MintURL = %q", est.MintURL)
	}
	if est.PricePerSecond != 10 {
		t.Errorf("PricePerSecond = %d", est.PricePerSecond)
	}
	if est.EstimatedSecs != 300 {
		t.Errorf("EstimatedSecs = %d", est.EstimatedSecs)
	}
}

func TestEstimateCost_Zero(t *testing.T) {
	est := EstimateCost(WorkerPricing{PricePerSecond: 5, Unit: "sat"}, 0)
	if est.EstimatedCost != 0 {
		t.Errorf("EstimatedCost = %d, want 0", est.EstimatedCost)
	}
}

func TestPaymentStatusValues(t *testing.T) {
	statuses := []PaymentStatus{
		PaymentStatusPending, PaymentStatusSent,
		PaymentStatusRedeemed, PaymentStatusFailed, PaymentStatusRefunded,
	}
	for _, s := range statuses {
		if s == "" {
			t.Error("empty status value")
		}
	}
}

func TestPaymentDirectionValues(t *testing.T) {
	if PaymentDirectionPayment != "payment" {
		t.Error("wrong payment direction value")
	}
	if PaymentDirectionChange != "change" {
		t.Error("wrong change direction value")
	}
}
