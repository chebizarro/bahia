package controlplane

import "errors"

var (
	ErrResponderNotConfigured      = errors.New("control-plane responder is not configured")
	ErrResponderCorrelationMissing = errors.New("control-plane response correlation is missing")
	ErrResponderInvalidInput       = errors.New("control-plane responder input is invalid")
	ErrResponderNoRelayAccepted    = errors.New("no relay accepted control-plane response")
)
