package service

import (
	"context"
	"errors"
	"strings"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ErrAssistantToolCallRefused is wrapped by an MCP server adapter when a tool
// call was refused (for example by the operator allowlist) before anything
// was submitted. It is a definite failure, never an ambiguous dispatch.
var ErrAssistantToolCallRefused = errors.New("assistant tool call refused before submission")

// assistantWorkOperator is the operator the assistant acts as for one work
// item, read only from persisted execution state: the operator who approved
// approval-bound work, otherwise the operator who requested the turn.
func assistantWorkOperator(x domain.AssistantExecution, w domain.AssistantWorkItem) string {
	if w.Authorization != nil && strings.TrimSpace(w.Authorization.OperatorPubkey) != "" {
		return w.Authorization.OperatorPubkey
	}
	return x.OperatorPubkey
}

// assistantOperatorContext attaches the principal the assistant uses when it
// calls tools for an operator. It has the same shape the Nostr control plane
// gives an operator's own requests (NIP-98 method, pubkey subject, no roles),
// so MCP applies that operator's allowlist entry and tenant permissions and
// never more: it is deliberately not auth.SystemPrincipal. Any principal
// already in ctx is replaced; without an operator the principal is cleared
// and MCP refuses the call.
func assistantOperatorContext(ctx context.Context, operatorPubkey string) context.Context {
	pubkey := strings.ToLower(strings.TrimSpace(operatorPubkey))
	if pubkey == "" {
		return auth.ContextWithPrincipal(ctx, nil)
	}
	return auth.ContextWithPrincipal(ctx, &auth.Principal{Subject: pubkey, Method: auth.MethodNIP98, PubKey: pubkey})
}
