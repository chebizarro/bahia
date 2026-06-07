package handlers

import (
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

const commandReceiptTimeoutSeconds = 30

func writeAcceptedCommandReceipt(w http.ResponseWriter, receipt dto.CommandReceipt) {
	if receipt.TimeoutSeconds == 0 {
		receipt.TimeoutSeconds = commandReceiptTimeoutSeconds
	}
	if strings.TrimSpace(receipt.Message) == "" {
		receipt.Message = "request published; subscribe to Nostr result/read-model events for completion"
	}
	writeData(w, http.StatusAccepted, receipt)
}

func writeCommandPublishError(w http.ResponseWriter, err error) {
	if err == nil {
		writeError(w, http.StatusBadGateway, "command publish failed")
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "required") || strings.Contains(msg, "invalid") {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	writeError(w, http.StatusBadGateway, msg)
}

func commandReceiptFromService(receipt *controlplane.ServiceCommandReceipt) dto.CommandReceipt {
	if receipt == nil {
		return dto.CommandReceipt{}
	}
	readModels := map[string]int{}
	if receipt.RegistryKind != 0 {
		readModels["registry"] = receipt.RegistryKind
	}
	if receipt.StateKind != 0 {
		readModels["state"] = receipt.StateKind
	}
	return dto.CommandReceipt{
		RequestEventID:  receipt.RequestEventID,
		RequestPubkey:   receipt.RequestPubkey,
		RequestKind:     receipt.RequestKind,
		StatusKind:      receipt.StatusKind,
		ResultKind:      receipt.ResultKind,
		ReadModelKinds:  nonEmptyReadModels(readModels),
		DTag:            receipt.DTag,
		IdempotencyKey:  receipt.IdempotencyKey,
		Status:          receipt.Status,
		Error:           receipt.Error,
		RetryHint:       receipt.RetryHint,
		PublishedRelays: receipt.PublishedRelays,
		TimeoutSeconds:  receipt.TimeoutSeconds,
	}
}

func commandReceiptFromLLM(receipt *controlplane.LLMCommandReceipt) dto.CommandReceipt {
	if receipt == nil {
		return dto.CommandReceipt{}
	}
	return dto.CommandReceipt{
		RequestEventID:  receipt.RequestEventID,
		RequestPubkey:   receipt.RequestPubkey,
		RequestKind:     receipt.RequestKind,
		StatusKind:      receipt.StatusKind,
		ResultKind:      receipt.ResultKind,
		ReadModelKinds:  nonEmptyReadModels(map[string]int{"registry": receipt.RegistryKind, "state": receipt.StateKind}),
		DTag:            receipt.DTag,
		IdempotencyKey:  receipt.IdempotencyKey,
		Status:          receipt.Status,
		Error:           receipt.Error,
		RetryHint:       receipt.RetryHint,
		PublishedRelays: receipt.PublishedRelays,
		TimeoutSeconds:  receipt.TimeoutSeconds,
	}
}

func commandReceiptFromPolicy(receipt *controlplane.PolicyCommandReceipt) dto.CommandReceipt {
	if receipt == nil {
		return dto.CommandReceipt{}
	}
	return dto.CommandReceipt{
		RequestEventID:  receipt.RequestEventID,
		RequestPubkey:   receipt.RequestPubkey,
		RequestKind:     receipt.RequestKind,
		StatusKind:      receipt.StatusKind,
		ResultKind:      receipt.ResultKind,
		ReadModelKinds:  receipt.ReadModelKinds,
		DTag:            receipt.DTag,
		IdempotencyKey:  receipt.IdempotencyKey,
		Status:          receipt.Status,
		Error:           receipt.Error,
		RetryHint:       receipt.RetryHint,
		PublishedRelays: receipt.PublishedRelays,
		TimeoutSeconds:  receipt.TimeoutSeconds,
	}
}

func nonEmptyReadModels(in map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		if strings.TrimSpace(k) != "" && v != 0 {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
