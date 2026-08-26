package relaysidecar

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip86"
	"github.com/openagentsinc/bahia/internal/config"
)

const (
	nip86ContentType = "application/nostr+json+rpc"
	nip98Kind        = nostr.Kind(27235)
	nip98Window      = 60 * time.Second
)

var sidecarNIP86Methods = []string{
	"allowpubkey",
	"banpubkey",
	"changerelaydescription",
	"changerelayicon",
	"changerelayname",
	"listallowedpubkeys",
	"listbannedpubkeys",
	"configstatus",
	"reload",
	"supportedmethods",
}

type pubkeyPolicyEntry struct {
	Pubkey string `json:"pubkey"`
	Reason string `json:"reason,omitempty"`
}

type relayMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon,omitempty"`
}

type adminPolicyState struct {
	Version            int                          `json:"version"`
	Administrators     []string                     `json:"administrators"`
	AllowedPubkeys     []pubkeyPolicyEntry          `json:"allowed_pubkeys"`
	BannedPubkeys      []pubkeyPolicyEntry          `json:"banned_pubkeys"`
	Metadata           relayMetadata                `json:"metadata"`
	UsedAuthorizations map[string]int64             `json:"used_authorizations,omitempty"`
	AppliedConfig      map[string]appliedCoordinate `json:"applied_config,omitempty"`
}

type appliedCoordinate struct {
	Author  string `json:"author"`
	EventID string `json:"event_id"`
	Version int    `json:"version"`
}

type adminPolicy struct {
	mu            sync.RWMutex
	path          string
	state         adminPolicyState
	now           func() time.Time
	lastReload    time.Time
	lastRejection string
}

func openAdminPolicy(cfg config.RelaySidecarConfig) (*adminPolicy, error) {
	path := strings.TrimSpace(cfg.AdminPolicyPath)
	if path == "" {
		path = filepath.Join(cfg.DataDir, "relay-admin-policy.json")
	}
	policy := &adminPolicy{path: path, now: time.Now}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &policy.state); err != nil {
			return nil, fmt.Errorf("parse relay administrator policy %s: %w", path, err)
		}
		if err := validateAdminPolicyState(&policy.state); err != nil {
			return nil, fmt.Errorf("validate relay administrator policy %s: %w", path, err)
		}
		return policy, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read relay administrator policy %s: %w", path, err)
	}
	admins, err := normalizePubkeys(cfg.AdministratorPubkeys)
	if err != nil {
		return nil, fmt.Errorf("seed relay administrator policy: %w", err)
	}
	policy.state = adminPolicyState{
		Version:            1,
		Administrators:     admins,
		AllowedPubkeys:     []pubkeyPolicyEntry{},
		BannedPubkeys:      []pubkeyPolicyEntry{},
		Metadata:           relayMetadata{Name: "Bahia Relay Sidecar", Description: "Local Khatru relay sidecar for Bahia browser bootstrap and Nostr events."},
		UsedAuthorizations: map[string]int64{},
		AppliedConfig:      map[string]appliedCoordinate{},
	}
	if err := policy.persist(policy.state); err != nil {
		return nil, fmt.Errorf("persist initial relay administrator policy: %w", err)
	}
	return policy, nil
}

func validateAdminPolicyState(state *adminPolicyState) error {
	if state.Version != 1 {
		return fmt.Errorf("unsupported policy version %d", state.Version)
	}
	admins, err := normalizePubkeys(state.Administrators)
	if err != nil {
		return fmt.Errorf("administrators: %w", err)
	}
	state.Administrators = admins
	if state.AllowedPubkeys == nil {
		state.AllowedPubkeys = []pubkeyPolicyEntry{}
	}
	if state.BannedPubkeys == nil {
		state.BannedPubkeys = []pubkeyPolicyEntry{}
	}
	if state.UsedAuthorizations == nil {
		state.UsedAuthorizations = map[string]int64{}
	}
	if state.AppliedConfig == nil {
		state.AppliedConfig = map[string]appliedCoordinate{}
	}
	for _, list := range [][]pubkeyPolicyEntry{state.AllowedPubkeys, state.BannedPubkeys} {
		for _, entry := range list {
			if _, err := nostr.PubKeyFromHex(entry.Pubkey); err != nil {
				return fmt.Errorf("invalid policy pubkey %q", entry.Pubkey)
			}
		}
	}
	return nil
}

func normalizePubkeys(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, err := nostr.PubKeyFromHex(value); err != nil {
			return nil, fmt.Errorf("pubkey %q must be 64 hex characters", value)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func (p *adminPolicy) persist(state adminPolicyState) error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(p.path)
	tmp, err := os.CreateTemp(dir, ".relay-admin-policy-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, p.path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (p *adminPolicy) snapshot() adminPolicyState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	state := p.state
	state.Administrators = append([]string(nil), p.state.Administrators...)
	state.AllowedPubkeys = append([]pubkeyPolicyEntry(nil), p.state.AllowedPubkeys...)
	state.BannedPubkeys = append([]pubkeyPolicyEntry(nil), p.state.BannedPubkeys...)
	state.UsedAuthorizations = cloneStringInt64Map(p.state.UsedAuthorizations)
	state.AppliedConfig = cloneCoordinates(p.state.AppliedConfig)
	return state
}

func cloneStringInt64Map(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneCoordinates(in map[string]appliedCoordinate) map[string]appliedCoordinate {
	out := make(map[string]appliedCoordinate, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (p *adminPolicy) authorize(event nostr.Event) error {
	now := p.now().UTC()
	created := event.CreatedAt.Time()
	if created.Before(now.Add(-nip98Window)) || created.After(now.Add(nip98Window)) {
		return fmt.Errorf("NIP-98 created_at is outside the %s acceptance window", nip98Window)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	authorized := false
	for _, admin := range p.state.Administrators {
		if admin == event.PubKey.Hex() {
			authorized = true
			break
		}
	}
	if !authorized {
		return fmt.Errorf("NIP-98 signer is not in the persisted administrator allowlist")
	}
	next := p.state
	next.UsedAuthorizations = cloneStringInt64Map(p.state.UsedAuthorizations)
	cutoff := now.Add(-nip98Window).Unix()
	for id, expires := range next.UsedAuthorizations {
		if expires < cutoff {
			delete(next.UsedAuthorizations, id)
		}
	}
	if _, replayed := next.UsedAuthorizations[event.ID.Hex()]; replayed {
		return fmt.Errorf("NIP-98 authorization event was already used")
	}
	next.UsedAuthorizations[event.ID.Hex()] = now.Add(nip98Window).Unix()
	if err := p.persist(next); err != nil {
		return fmt.Errorf("persist NIP-98 replay state: %w", err)
	}
	p.state = next
	return nil
}

func (p *adminPolicy) admits(pubkey string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, entry := range p.state.BannedPubkeys {
		if entry.Pubkey == pubkey {
			return false
		}
	}
	if len(p.state.AllowedPubkeys) == 0 {
		return true
	}
	for _, entry := range p.state.AllowedPubkeys {
		if entry.Pubkey == pubkey {
			return true
		}
	}
	return false
}

func (p *adminPolicy) mutatePubkey(pubkey nostr.PubKey, reason string, allow bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state
	next.AllowedPubkeys = append([]pubkeyPolicyEntry(nil), p.state.AllowedPubkeys...)
	next.BannedPubkeys = append([]pubkeyPolicyEntry(nil), p.state.BannedPubkeys...)
	value := pubkey.Hex()
	if allow {
		next.AllowedPubkeys = upsertPubkey(next.AllowedPubkeys, value, reason)
		next.BannedPubkeys = removePubkey(next.BannedPubkeys, value)
	} else {
		next.BannedPubkeys = upsertPubkey(next.BannedPubkeys, value, reason)
		next.AllowedPubkeys = removePubkey(next.AllowedPubkeys, value)
	}
	if err := p.persist(next); err != nil {
		return fmt.Errorf("persist relay pubkey policy: %w", err)
	}
	p.state = next
	return nil
}

func upsertPubkey(entries []pubkeyPolicyEntry, pubkey, reason string) []pubkeyPolicyEntry {
	for i := range entries {
		if entries[i].Pubkey == pubkey {
			entries[i].Reason = strings.TrimSpace(reason)
			return entries
		}
	}
	entries = append(entries, pubkeyPolicyEntry{Pubkey: pubkey, Reason: strings.TrimSpace(reason)})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Pubkey < entries[j].Pubkey })
	return entries
}

func removePubkey(entries []pubkeyPolicyEntry, pubkey string) []pubkeyPolicyEntry {
	out := entries[:0]
	for _, entry := range entries {
		if entry.Pubkey != pubkey {
			out = append(out, entry)
		}
	}
	return out
}

func (p *adminPolicy) changeMetadata(update func(*relayMetadata)) (relayMetadata, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state
	update(&next.Metadata)
	if err := p.persist(next); err != nil {
		return relayMetadata{}, fmt.Errorf("persist relay metadata: %w", err)
	}
	p.state = next
	return next.Metadata, nil
}

func (p *adminPolicy) reload() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		p.mu.Lock()
		p.lastRejection = err.Error()
		p.mu.Unlock()
		return fmt.Errorf("read persisted relay policy: %w", err)
	}
	var next adminPolicyState
	if err := json.Unmarshal(data, &next); err != nil {
		p.mu.Lock()
		p.lastRejection = err.Error()
		p.mu.Unlock()
		return fmt.Errorf("parse persisted relay policy: %w", err)
	}
	if err := validateAdminPolicyState(&next); err != nil {
		p.mu.Lock()
		p.lastRejection = err.Error()
		p.mu.Unlock()
		return err
	}
	p.mu.Lock()
	p.state = next
	p.lastReload = p.now().UTC()
	p.lastRejection = ""
	p.mu.Unlock()
	return nil
}

func (p *adminPolicy) configStatus() map[string]any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	effectiveVersion := 0
	lastEventID := ""
	for _, coordinate := range p.state.AppliedConfig {
		if coordinate.Version > effectiveVersion {
			effectiveVersion = coordinate.Version
			lastEventID = coordinate.EventID
		}
	}
	return map[string]any{
		"effective_schema":      configRelaySchema,
		"effective_version":     effectiveVersion,
		"last_applied_event_id": lastEventID,
		"health":                "ok",
		"reload_time":           p.lastReload,
		"last_rejection":        p.lastRejection,
		"drift":                 false,
		"allowed_pubkeys":       len(p.state.AllowedPubkeys),
		"banned_pubkeys":        len(p.state.BannedPubkeys),
	}
}

func (p *adminPolicy) applyConfigProjection(projection ConfigProjection) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state
	next.AllowedPubkeys = append([]pubkeyPolicyEntry(nil), p.state.AllowedPubkeys...)
	next.BannedPubkeys = append([]pubkeyPolicyEntry(nil), p.state.BannedPubkeys...)
	if projection.PolicyName == "membership" {
		next.AllowedPubkeys = make([]pubkeyPolicyEntry, 0, len(projection.AllowedPubkeys))
		for _, pubkey := range projection.AllowedPubkeys {
			next.AllowedPubkeys = append(next.AllowedPubkeys, pubkeyPolicyEntry{Pubkey: pubkey, Reason: "config-fabric membership"})
		}
	} else {
		next.AllowedPubkeys = make([]pubkeyPolicyEntry, 0, len(projection.AllowedPubkeys))
		for _, pubkey := range projection.AllowedPubkeys {
			next.AllowedPubkeys = append(next.AllowedPubkeys, pubkeyPolicyEntry{Pubkey: pubkey, Reason: "config-fabric relay policy"})
		}
		next.BannedPubkeys = make([]pubkeyPolicyEntry, 0, len(projection.BannedPubkeys))
		for _, pubkey := range projection.BannedPubkeys {
			next.BannedPubkeys = append(next.BannedPubkeys, pubkeyPolicyEntry{Pubkey: pubkey, Reason: "config-fabric relay policy"})
		}
		if projection.RelayName != nil {
			next.Metadata.Name = *projection.RelayName
		}
		if projection.RelayDescription != nil {
			next.Metadata.Description = *projection.RelayDescription
		}
		if projection.RelayIcon != nil {
			next.Metadata.Icon = *projection.RelayIcon
		}
	}
	next.AppliedConfig = cloneCoordinates(p.state.AppliedConfig)
	coordinate := projection.Author + "\x00" + projection.ServiceID + "\x00" + projection.Scope + "\x00" + projection.PolicyName
	next.AppliedConfig[coordinate] = appliedCoordinate{Author: projection.Author, EventID: projection.EventID, Version: projection.Version}
	if err := p.persist(next); err != nil {
		return fmt.Errorf("persist config-fabric relay policy: %w", err)
	}
	p.state = next
	return nil
}

func (p *adminPolicy) listPubkeys(allowed bool) []nip86.PubKeyReason {
	state := p.snapshot()
	entries := state.BannedPubkeys
	if allowed {
		entries = state.AllowedPubkeys
	}
	out := make([]nip86.PubKeyReason, 0, len(entries))
	for _, entry := range entries {
		pubkey, _ := nostr.PubKeyFromHex(entry.Pubkey)
		out = append(out, nip86.PubKeyReason{PubKey: pubkey, Reason: entry.Reason})
	}
	return out
}

func (s *Server) handleNIP86(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != nip86ContentType {
		http.Error(w, "Content-Type must be "+nip86ContentType, http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read NIP-86 request body", http.StatusBadRequest)
		return
	}
	authEvent, err := validateNIP98Authorization(r.Header.Get("Authorization"), s.cfg.PublicURL, body, s.policy.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if err := s.policy.authorize(authEvent); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var request nip86.Request
	if err := json.Unmarshal(body, &request); err != nil {
		writeNIP86Response(w, http.StatusBadRequest, nip86.Response{Error: "invalid NIP-86 JSON request"})
		return
	}
	if request.Method == "configstatus" {
		writeNIP86Response(w, http.StatusOK, nip86.Response{Result: s.policy.configStatus()})
		return
	}
	if request.Method == "reload" {
		if err := s.policy.reload(); err != nil {
			writeNIP86Response(w, http.StatusBadRequest, nip86.Response{Error: err.Error()})
			return
		}
		s.applyMetadata(s.policy.snapshot().Metadata)
		writeNIP86Response(w, http.StatusOK, nip86.Response{Result: true})
		return
	}
	params, err := nip86.DecodeRequest(request)
	if err != nil {
		writeNIP86Response(w, http.StatusBadRequest, nip86.Response{Error: err.Error()})
		return
	}
	response := nip86.Response{}
	switch value := params.(type) {
	case nip86.SupportedMethods:
		response.Result = append([]string(nil), sidecarNIP86Methods...)
	case nip86.AllowPubKey:
		err = s.policy.mutatePubkey(value.PubKey, value.Reason, true)
		response.Result = true
	case nip86.BanPubKey:
		err = s.policy.mutatePubkey(value.PubKey, value.Reason, false)
		response.Result = true
	case nip86.ListAllowedPubKeys:
		response.Result = s.policy.listPubkeys(true)
	case nip86.ListBannedPubKeys:
		response.Result = s.policy.listPubkeys(false)
	case nip86.ChangeRelayName:
		var metadata relayMetadata
		metadata, err = s.policy.changeMetadata(func(current *relayMetadata) { current.Name = value.Name })
		if err == nil {
			s.applyMetadata(metadata)
			response.Result = true
		}
	case nip86.ChangeRelayDescription:
		var metadata relayMetadata
		metadata, err = s.policy.changeMetadata(func(current *relayMetadata) { current.Description = value.Description })
		if err == nil {
			s.applyMetadata(metadata)
			response.Result = true
		}
	case nip86.ChangeRelayIcon:
		var metadata relayMetadata
		metadata, err = s.policy.changeMetadata(func(current *relayMetadata) { current.Icon = value.IconURL })
		if err == nil {
			s.applyMetadata(metadata)
			response.Result = true
		}
	default:
		writeNIP86Response(w, http.StatusBadRequest, nip86.Response{Error: "unsupported NIP-86 method"})
		return
	}
	if err != nil {
		writeNIP86Response(w, http.StatusInternalServerError, nip86.Response{Error: err.Error()})
		return
	}
	writeNIP86Response(w, http.StatusOK, response)
}

func writeNIP86Response(w http.ResponseWriter, status int, response nip86.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func validateNIP98Authorization(header, canonicalURL string, body []byte, now time.Time) (nostr.Event, error) {
	const prefix = "Nostr "
	if !strings.HasPrefix(header, prefix) {
		return nostr.Event{}, fmt.Errorf("missing Nostr NIP-98 authorization")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	if err != nil {
		return nostr.Event{}, fmt.Errorf("decode NIP-98 authorization: %w", err)
	}
	var event nostr.Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return nostr.Event{}, fmt.Errorf("decode NIP-98 event: %w", err)
	}
	if event.Kind != nip98Kind {
		return nostr.Event{}, fmt.Errorf("NIP-98 event kind must be 27235")
	}
	if !event.CheckID() {
		return nostr.Event{}, fmt.Errorf("NIP-98 event id is invalid")
	}
	if !event.VerifySignature() {
		return nostr.Event{}, fmt.Errorf("NIP-98 event signature is invalid")
	}
	if created := event.CreatedAt.Time(); created.Before(now.Add(-nip98Window)) || created.After(now.Add(nip98Window)) {
		return nostr.Event{}, fmt.Errorf("NIP-98 created_at is outside the acceptance window")
	}
	if value, err := exactlyOneEventTag(event.Tags, "u"); err != nil || value != strings.TrimSpace(canonicalURL) {
		return nostr.Event{}, fmt.Errorf("NIP-98 u tag does not match the canonical management URL")
	}
	if value, err := exactlyOneEventTag(event.Tags, "method"); err != nil || value != http.MethodPost {
		return nostr.Event{}, fmt.Errorf("NIP-98 method tag must be POST")
	}
	sum := sha256.Sum256(body)
	if value, err := exactlyOneEventTag(event.Tags, "payload"); err != nil || value != hex.EncodeToString(sum[:]) {
		return nostr.Event{}, fmt.Errorf("NIP-98 payload tag does not match the request body")
	}
	return event, nil
}

func exactlyOneEventTag(tags nostr.Tags, name string) (string, error) {
	var value string
	count := 0
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			count++
			value = tag[1]
		}
	}
	if count != 1 || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("NIP-98 event requires exactly one %s tag", name)
	}
	return value, nil
}
