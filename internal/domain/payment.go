package domain

import (
	"time"

	"github.com/google/uuid"
)

// PaymentDirection indicates whether a payment is outgoing (to worker) or incoming (change).
type PaymentDirection string

const (
	PaymentDirectionPayment PaymentDirection = "payment"
	PaymentDirectionChange  PaymentDirection = "change"
)

// PaymentStatus tracks the lifecycle of a Cashu payment.
type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "pending"
	PaymentStatusSent      PaymentStatus = "sent"
	PaymentStatusRedeemed  PaymentStatus = "redeemed"
	PaymentStatusFailed    PaymentStatus = "failed"
	PaymentStatusRefunded  PaymentStatus = "refunded"
)

// PaymentRecord stores a single Cashu payment or change event.
type PaymentRecord struct {
	ID              uuid.UUID        `json:"id"`
	DeploymentRunID uuid.UUID        `json:"deployment_run_id"`
	WorkerPubkey    string           `json:"worker_pubkey"`
	MintURL         string           `json:"mint_url"`
	AmountSats      int64            `json:"amount_sats"`
	TokenHash       string           `json:"token_hash,omitempty"`  // hash of the Cashu token for idempotency
	Direction       PaymentDirection `json:"direction"`
	Status          PaymentStatus    `json:"status"`
	ErrorMessage    string           `json:"error_message,omitempty"`
	Metadata        map[string]any   `json:"metadata,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// CostEstimate holds a cost estimate for a deployment run.
type CostEstimate struct {
	WorkerPubkey    string `json:"worker_pubkey"`
	WorkerName      string `json:"worker_name,omitempty"`
	MintURL         string `json:"mint_url"`
	PricePerSecond  int    `json:"price_per_second"`
	EstimatedSecs   int    `json:"estimated_secs"`
	EstimatedCost   int64  `json:"estimated_cost_sats"`
	Unit            string `json:"unit"`
}

// EstimateCost calculates the total cost from worker pricing and estimated duration.
func EstimateCost(pricing WorkerPricing, estimatedDurationSecs int) CostEstimate {
	total := int64(pricing.PricePerSecond) * int64(estimatedDurationSecs)
	return CostEstimate{
		MintURL:        pricing.MintURL,
		PricePerSecond: pricing.PricePerSecond,
		EstimatedSecs:  estimatedDurationSecs,
		EstimatedCost:  total,
		Unit:           pricing.Unit,
	}
}
