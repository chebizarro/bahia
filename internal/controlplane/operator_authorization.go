package controlplane

import (
	"slices"
	"strings"
)

func authorizedOperatorPubkey(pubkey string, authorized []string) bool {
	pubkey = strings.TrimSpace(pubkey)
	if pubkey == "" || len(authorized) == 0 {
		return false
	}
	return slices.ContainsFunc(authorized, func(allowed string) bool {
		return strings.EqualFold(strings.TrimSpace(allowed), pubkey)
	})
}
