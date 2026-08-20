package soulfactory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip46"
	cascadia "git.sharegap.net/cascadia/cascadia-go"

	"github.com/openagentsinc/bahia/internal/domain"
)

const OpenClawSignetIdentityContractSchema = "bahia.openclaw-signet-identity.v1"
const RuntimeSignetIdentityContractSchema = "bahia.runtime-signet-identity.v1"

var openClawNIP46Methods = []string{
	"connect",
	"get_public_key",
	"get_relays",
	"nip44_decrypt",
	"nip44_encrypt",
	"ping",
	"sign_event",
}

var openClawFleetEventKinds = []int{
	cascadia.NIP59_GIFT_WRAP,
	cascadia.CAS_AUDIT,
	cascadia.CAS_INTENT,
	cascadia.CAS_AGENT_HEARTBEAT,
	cascadia.CAS_AGENT_CAPABILITY,
}

// SignetEnrollmentProfile defines the exact NIP-46 authority granted to one
// enrollment class. Profiles never admit wildcard clients, methods, or kinds.
type SignetEnrollmentProfile struct {
	ContractSchema string
	Methods        []string
	EventKinds     []int
}

// MetiqRuntimeSignetEnrollmentProfile grants a dedicated Metiq bridge only
// the operations and event kinds needed to advertise capability and sign
// correlated runtime-control results.
func MetiqRuntimeSignetEnrollmentProfile() SignetEnrollmentProfile {
	return SignetEnrollmentProfile{
		ContractSchema: RuntimeSignetIdentityContractSchema,
		Methods:        []string{"connect", "get_public_key", "get_relays", "ping", "sign_event"},
		EventKinds:     []int{cascadia.CAS_AGENT_CAPABILITY, domain.KindRuntimeControlResult},
	}
}

// OpenClawSignetIdentityContract is the secret-free durable identity boundary
// shared by Bahia and the OpenClaw runtime bridge. BunkerURL never contains a
// one-time secret; ClientKeyRef names a protected file instead of key material.
type OpenClawSignetIdentityContract struct {
	Schema            string   `json:"schema"`
	AgentID           string   `json:"agent_id"`
	ControllerPubkey  string   `json:"controller_pubkey"`
	RuntimePubkey     string   `json:"runtime_pubkey"`
	ManagedPubkey     string   `json:"managed_pubkey"`
	ProvisionerPubkey string   `json:"provisioner_pubkey"`
	ClientPubkey      string   `json:"client_pubkey"`
	BunkerPubkey      string   `json:"bunker_pubkey"`
	BunkerURL         string   `json:"bunker_url"`
	ClientKeyRef      string   `json:"client_key_ref"`
	Relays            []string `json:"relays"`
	Methods           []string `json:"methods"`
	EventKinds        []int    `json:"event_kinds"`
	ConnectedAt       int64    `json:"connected_at"`
}

type OpenClawSignetEnrollmentRequest struct {
	AgentID           string
	ControllerPubkey  string
	RuntimePubkey     string
	ManagedPubkey     string
	ProvisionerPubkey string
	BunkerURI         string
	AllowedKinds      []int
}

type OpenClawSignetEnrollmentInspector interface {
	Inspect(context.Context, string) (*OpenClawSignetIdentityContract, error)
}

type OpenClawSignetEnrollment interface {
	OpenClawSignetEnrollmentInspector
	StageHandoff(context.Context, string, string) error
	Enroll(context.Context, OpenClawSignetEnrollmentRequest) (*OpenClawSignetIdentityContract, error)
	Reconcile(context.Context, OpenClawSignetEnrollmentRequest) (*OpenClawSignetIdentityContract, error)
	Revoke(context.Context, string) error
}

type SignetPolicyAdmin interface {
	SetPolicy(context.Context, string, SignetClientPolicy) error
	RevokeClient(context.Context, string) error
}

type SignetClientPolicy struct {
	ClientPubkey string
	Methods      []string
	EventKinds   []int
}

type SignetConnectivityVerifier interface {
	Verify(context.Context, string, string, string, []int) error
}

type OpenClawSignetEnrollmentConfig struct {
	StateDir     string
	ClientKeyDir string
	FileOwnerUID int
	PolicyAdmin  SignetPolicyAdmin
	Verifier     SignetConnectivityVerifier
	Now          func() time.Time
	Random       io.Reader
	Profile      *SignetEnrollmentProfile
}

type OpenClawSignetEnrollmentManager struct {
	config  OpenClawSignetEnrollmentConfig
	profile SignetEnrollmentProfile
}

func NewOpenClawSignetEnrollmentManager(config OpenClawSignetEnrollmentConfig) (*OpenClawSignetEnrollmentManager, error) {
	if !filepath.IsAbs(config.StateDir) || !filepath.IsAbs(config.ClientKeyDir) {
		return nil, fmt.Errorf("OpenClaw Signet state and client-key directories must be absolute")
	}
	if config.PolicyAdmin == nil || config.Verifier == nil {
		return nil, fmt.Errorf("OpenClaw Signet policy admin and connectivity verifier are required")
	}
	if config.FileOwnerUID < 0 {
		config.FileOwnerUID = os.Geteuid()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	profile := SignetEnrollmentProfile{
		ContractSchema: OpenClawSignetIdentityContractSchema,
		Methods:        append([]string(nil), openClawNIP46Methods...),
		EventKinds:     append([]int(nil), openClawFleetEventKinds...),
	}
	if config.Profile != nil {
		for _, method := range config.Profile.Methods {
			if strings.TrimSpace(method) == "*" {
				return nil, fmt.Errorf("Signet enrollment profile methods must not contain wildcards")
			}
		}
		profile = SignetEnrollmentProfile{
			ContractSchema: strings.TrimSpace(config.Profile.ContractSchema),
			Methods:        uniqueSortedStrings(config.Profile.Methods),
			EventKinds:     normalizedKinds(config.Profile.EventKinds),
		}
	}
	if profile.ContractSchema == "" || len(profile.Methods) == 0 || len(profile.EventKinds) == 0 {
		return nil, fmt.Errorf("Signet enrollment profile requires schema, methods, and event kinds")
	}
	return &OpenClawSignetEnrollmentManager{config: config, profile: profile}, nil
}

func (m *OpenClawSignetEnrollmentManager) StageHandoff(_ context.Context, agentID, bunkerURI string) error {
	if !safeAgentID(agentID) {
		return fmt.Errorf("invalid agent id")
	}
	if _, _, _, err := sanitizeOneTimeBunkerURI(bunkerURI); err != nil {
		return err
	}
	handoffPath, _, _ := m.paths(agentID)
	if err := writeProtectedFile(handoffPath, []byte(strings.TrimSpace(bunkerURI)+"\n"), m.config.FileOwnerUID); err != nil {
		return fmt.Errorf("store one-time bunker handoff: %w", err)
	}
	return nil
}

func (m *OpenClawSignetEnrollmentManager) Enroll(ctx context.Context, req OpenClawSignetEnrollmentRequest) (*OpenClawSignetIdentityContract, error) {
	if err := validateEnrollmentRequest(req); err != nil {
		return nil, err
	}
	if existing, err := m.Inspect(ctx, req.AgentID); err != nil {
		return nil, err
	} else if existing != nil {
		if err := matchEnrollmentIdentity(*existing, req); err != nil {
			return nil, err
		}
		desiredMethods := append([]string(nil), m.profile.Methods...)
		desiredKinds := normalizedKinds(append(append([]int(nil), req.AllowedKinds...), m.profile.EventKinds...))
		if err := m.config.PolicyAdmin.SetPolicy(ctx, req.AgentID, SignetClientPolicy{ClientPubkey: existing.ClientPubkey, Methods: desiredMethods, EventKinds: desiredKinds}); err != nil {
			return nil, fmt.Errorf("reconcile existing Signet client policy: %w", err)
		}
		if err := m.config.Verifier.Verify(ctx, existing.BunkerURL, existing.ClientKeyRef, existing.ManagedPubkey, desiredKinds); err != nil {
			return nil, fmt.Errorf("verify existing OpenClaw Signet enrollment: %w", err)
		}
		if !slices.Equal(existing.Methods, desiredMethods) || !slices.Equal(existing.EventKinds, desiredKinds) {
			existing.Methods = desiredMethods
			existing.EventKinds = desiredKinds
			_, _, statePath := m.paths(req.AgentID)
			if err := writeProtectedJSON(statePath, existing, m.config.FileOwnerUID); err != nil {
				return nil, fmt.Errorf("persist reconciled OpenClaw Signet enrollment: %w", err)
			}
		}
		handoffPath, _, _ := m.paths(req.AgentID)
		if err := os.Remove(handoffPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("delete consumed one-time bunker handoff after durable connectivity: %w", err)
		}
		return existing, nil
	}

	handoffPath, clientKeyPath, statePath := m.paths(req.AgentID)
	if strings.TrimSpace(req.BunkerURI) != "" {
		if err := m.StageHandoff(ctx, req.AgentID, req.BunkerURI); err != nil {
			return nil, err
		}
	}
	handoff, err := readProtectedFile(handoffPath, m.config.FileOwnerUID)
	if err != nil {
		return nil, fmt.Errorf("read protected one-time bunker handoff: %w", err)
	}
	bunkerURL, bunkerPubkey, relays, err := sanitizeOneTimeBunkerURI(strings.TrimSpace(string(handoff)))
	if err != nil {
		return nil, err
	}

	clientSecret, err := m.loadOrCreateClientKey(clientKeyPath)
	if err != nil {
		return nil, err
	}
	secretKey, err := nostr.SecretKeyFromHex(clientSecret)
	if err != nil {
		return nil, fmt.Errorf("decode durable NIP-46 client key: %w", err)
	}
	clientPubkey := secretKey.Public().Hex()
	methods := append([]string(nil), m.profile.Methods...)
	kinds := normalizedKinds(append(append([]int(nil), req.AllowedKinds...), m.profile.EventKinds...))
	policy := SignetClientPolicy{ClientPubkey: clientPubkey, Methods: methods, EventKinds: kinds}
	if err := m.config.PolicyAdmin.SetPolicy(ctx, req.AgentID, policy); err != nil {
		return nil, fmt.Errorf("set exact-client Signet policy: %w", err)
	}

	if err := m.config.Verifier.Verify(ctx, handoffPath, clientKeyPath, req.ManagedPubkey, kinds); err != nil {
		cleanupErr := m.config.PolicyAdmin.RevokeClient(ctx, clientPubkey)
		if cleanupErr != nil {
			return nil, errors.Join(fmt.Errorf("verify new OpenClaw Signet enrollment: %w", err), fmt.Errorf("compensating client revoke: %w", cleanupErr))
		}
		return nil, fmt.Errorf("verify new OpenClaw Signet enrollment: %w", err)
	}

	contract := &OpenClawSignetIdentityContract{
		Schema: m.profile.ContractSchema, AgentID: req.AgentID,
		ControllerPubkey: strings.ToLower(req.ControllerPubkey), RuntimePubkey: strings.ToLower(req.RuntimePubkey),
		ManagedPubkey: strings.ToLower(req.ManagedPubkey), ProvisionerPubkey: strings.ToLower(req.ProvisionerPubkey),
		ClientPubkey: clientPubkey, BunkerPubkey: bunkerPubkey, BunkerURL: bunkerURL,
		ClientKeyRef: clientKeyPath, Relays: relays, Methods: methods, EventKinds: kinds,
		ConnectedAt: m.config.Now().UTC().Unix(),
	}
	if err := writeProtectedJSON(statePath, contract, m.config.FileOwnerUID); err != nil {
		cleanupErr := m.config.PolicyAdmin.RevokeClient(ctx, clientPubkey)
		return nil, errors.Join(fmt.Errorf("persist durable OpenClaw Signet enrollment: %w", err), cleanupErr)
	}
	if err := os.Remove(handoffPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("delete consumed one-time bunker handoff after durable connectivity: %w", err)
	}
	return contract, nil
}

func (m *OpenClawSignetEnrollmentManager) Reconcile(ctx context.Context, req OpenClawSignetEnrollmentRequest) (*OpenClawSignetIdentityContract, error) {
	return m.Enroll(ctx, req)
}

func (m *OpenClawSignetEnrollmentManager) Inspect(_ context.Context, agentID string) (*OpenClawSignetIdentityContract, error) {
	if !safeAgentID(agentID) {
		return nil, fmt.Errorf("invalid agent id")
	}
	_, _, statePath := m.paths(agentID)
	data, err := readProtectedFile(statePath, m.config.FileOwnerUID)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read OpenClaw Signet enrollment state: %w", err)
	}
	var state OpenClawSignetIdentityContract
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse OpenClaw Signet enrollment state: %w", err)
	}
	if state.Schema != m.profile.ContractSchema || state.AgentID != agentID {
		return nil, fmt.Errorf("OpenClaw Signet enrollment state identity mismatch")
	}
	if _, err := readProtectedFile(state.ClientKeyRef, m.config.FileOwnerUID); err != nil {
		return nil, fmt.Errorf("validate durable NIP-46 client key reference: %w", err)
	}
	return &state, nil
}

func (m *OpenClawSignetEnrollmentManager) Revoke(ctx context.Context, agentID string) error {
	state, err := m.Inspect(ctx, agentID)
	if err != nil {
		return err
	}
	handoffPath, clientKeyPath, statePath := m.paths(agentID)
	if state != nil {
		if err := m.config.PolicyAdmin.RevokeClient(ctx, state.ClientPubkey); err != nil {
			return fmt.Errorf("revoke exact Signet client: %w", err)
		}
		clientKeyPath = state.ClientKeyRef
	} else if clientKey, readErr := readProtectedFile(clientKeyPath, m.config.FileOwnerUID); readErr == nil {
		secretKey, decodeErr := nostr.SecretKeyFromHex(strings.TrimSpace(string(clientKey)))
		if decodeErr != nil {
			return fmt.Errorf("decode orphaned NIP-46 client key: %w", decodeErr)
		}
		if err := m.config.PolicyAdmin.RevokeClient(ctx, secretKey.Public().Hex()); err != nil {
			return fmt.Errorf("revoke orphaned exact Signet client: %w", err)
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read orphaned NIP-46 client key: %w", readErr)
	}
	for _, path := range []string{handoffPath, clientKeyPath, statePath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove revoked OpenClaw Signet material %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func (m *OpenClawSignetEnrollmentManager) paths(agentID string) (string, string, string) {
	return filepath.Join(m.config.StateDir, agentID+".bunker-once"),
		filepath.Join(m.config.ClientKeyDir, agentID+".nip46-client"),
		filepath.Join(m.config.StateDir, agentID+".json")
}

func (m *OpenClawSignetEnrollmentManager) loadOrCreateClientKey(path string) (string, error) {
	data, err := readProtectedFile(path, m.config.FileOwnerUID)
	if err == nil {
		value := strings.TrimSpace(string(data))
		if _, err := nostr.SecretKeyFromHex(value); err != nil {
			return "", fmt.Errorf("decode existing NIP-46 client key: %w", err)
		}
		return value, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read existing NIP-46 client key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(m.config.Random, key); err != nil {
		return "", fmt.Errorf("generate NIP-46 client key: %w", err)
	}
	value := hex.EncodeToString(key)
	for i := range key {
		key[i] = 0
	}
	if err := writeProtectedFile(path, []byte(value+"\n"), m.config.FileOwnerUID); err != nil {
		return "", fmt.Errorf("persist NIP-46 client key: %w", err)
	}
	return value, nil
}

type NIP46ConnectivityVerifier struct{}

func (NIP46ConnectivityVerifier) Verify(ctx context.Context, bunkerURIOrFile, clientKeyFile, expectedPubkey string, eventKinds []int) error {
	bunkerURI := bunkerURIOrFile
	if !strings.HasPrefix(bunkerURI, "bunker://") {
		data, err := readProtectedFile(clientPathClean(bunkerURI), os.Geteuid())
		if err != nil {
			return err
		}
		bunkerURI = strings.TrimSpace(string(data))
	}
	keyData, err := readProtectedFile(clientPathClean(clientKeyFile), os.Geteuid())
	if err != nil {
		return err
	}
	clientKey, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(keyData)))
	if err != nil {
		return fmt.Errorf("decode NIP-46 client key: %w", err)
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	bunker, err := nip46.ConnectBunker(lifetime, clientKey, bunkerURI, nil, nil)
	if err != nil {
		return fmt.Errorf("connect NIP-46 bunker: %w", err)
	}
	pubkey, err := bunker.GetPublicKey(ctx)
	if err != nil {
		return fmt.Errorf("get NIP-46 managed pubkey: %w", err)
	}
	if pubkey.Hex() != strings.ToLower(strings.TrimSpace(expectedPubkey)) {
		return fmt.Errorf("NIP-46 managed pubkey %s does not match expected %s", pubkey.Hex(), expectedPubkey)
	}
	if err := bunker.Ping(ctx); err != nil {
		return fmt.Errorf("ping NIP-46 bunker: %w", err)
	}
	if len(eventKinds) > 0 {
		event := &nostr.Event{Kind: nostr.Kind(eventKinds[0]), CreatedAt: nostr.Now(), Tags: nostr.Tags{}, Content: ""}
		if err := bunker.SignEvent(ctx, event); err != nil {
			return fmt.Errorf("verify NIP-46 sign_event: %w", err)
		}
	}
	ciphertext, err := bunker.NIP44Encrypt(ctx, pubkey, "bahia-openclaw-enrollment")
	if err != nil {
		return fmt.Errorf("verify NIP-46 nip44_encrypt: %w", err)
	}
	plaintext, err := bunker.NIP44Decrypt(ctx, pubkey, ciphertext)
	if err != nil {
		return fmt.Errorf("verify NIP-46 nip44_decrypt: %w", err)
	}
	if plaintext != "bahia-openclaw-enrollment" {
		return fmt.Errorf("verify NIP-46 nip44_decrypt: plaintext mismatch")
	}
	return nil
}

// NIP46SigningConnectivityVerifier verifies the signing-only authority used by
// a runtime bridge without requesting unrelated NIP-44 permissions.
type NIP46SigningConnectivityVerifier struct{}

func (NIP46SigningConnectivityVerifier) Verify(ctx context.Context, bunkerURIOrFile, clientKeyFile, expectedPubkey string, eventKinds []int) error {
	bunkerURI := bunkerURIOrFile
	if !strings.HasPrefix(bunkerURI, "bunker://") {
		data, err := readProtectedFile(clientPathClean(bunkerURI), os.Geteuid())
		if err != nil {
			return err
		}
		bunkerURI = strings.TrimSpace(string(data))
	}
	keyData, err := readProtectedFile(clientPathClean(clientKeyFile), os.Geteuid())
	if err != nil {
		return err
	}
	clientKey, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(keyData)))
	if err != nil {
		return fmt.Errorf("decode NIP-46 client key: %w", err)
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	bunker, err := nip46.ConnectBunker(lifetime, clientKey, bunkerURI, nil, nil)
	if err != nil {
		return fmt.Errorf("connect NIP-46 bunker: %w", err)
	}
	pubkey, err := bunker.GetPublicKey(ctx)
	if err != nil {
		return fmt.Errorf("get NIP-46 managed pubkey: %w", err)
	}
	if pubkey.Hex() != strings.ToLower(strings.TrimSpace(expectedPubkey)) {
		return fmt.Errorf("NIP-46 managed pubkey %s does not match expected %s", pubkey.Hex(), expectedPubkey)
	}
	if err := bunker.Ping(ctx); err != nil {
		return fmt.Errorf("ping NIP-46 bunker: %w", err)
	}
	if len(eventKinds) == 0 {
		return fmt.Errorf("NIP-46 signing verification requires at least one allowed event kind")
	}
	event := &nostr.Event{Kind: nostr.Kind(eventKinds[0]), CreatedAt: nostr.Now(), Tags: nostr.Tags{}, Content: ""}
	if err := bunker.SignEvent(ctx, event); err != nil {
		return fmt.Errorf("verify NIP-46 sign_event: %w", err)
	}
	return nil
}

type SignetctlConfig struct {
	DockerBin                 string
	Container                 string
	ConfigPath                string
	ProvisionerCredentialFile string
	CredentialOwnerUID        int
	Runner                    SignetctlRunner
}

type SignetctlRunner interface {
	Run(context.Context, string, []string, []byte) ([]byte, error)
}

type ExecSignetctlRunner struct{}

func (ExecSignetctlRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		// signetctl failures can include echoed RPC payloads. Keep stderr out of
		// host logs so a failed provision cannot expose a one-time bunker URI.
		return nil, fmt.Errorf("signetctl failed: %w", err)
	}
	return out, nil
}

type ContainerSignetctl struct{ config SignetctlConfig }

func NewContainerSignetctl(config SignetctlConfig) (*ContainerSignetctl, error) {
	if config.DockerBin == "" {
		config.DockerBin = "docker"
	}
	if config.Container == "" || config.ConfigPath == "" || !filepath.IsAbs(config.ProvisionerCredentialFile) {
		return nil, fmt.Errorf("Signet container, config path, and absolute provisioner credential file are required")
	}
	if config.CredentialOwnerUID < 0 {
		config.CredentialOwnerUID = os.Geteuid()
	}
	if config.Runner == nil {
		config.Runner = ExecSignetctlRunner{}
	}
	return &ContainerSignetctl{config: config}, nil
}

func (c *ContainerSignetctl) SetPolicy(ctx context.Context, agentID string, policy SignetClientPolicy) error {
	if !isHexPubkey(policy.ClientPubkey) || policy.ClientPubkey == strings.Repeat("*", 64) {
		return fmt.Errorf("exact NIP-46 client pubkey is required")
	}
	payload, err := json.Marshal(map[string]interface{}{
		"default": false, "allow_clients": []string{strings.ToLower(policy.ClientPubkey)},
		"allow_methods": uniqueSortedStrings(policy.Methods), "allow_kinds": normalizedKinds(policy.EventKinds),
	})
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "set-policy", agentID, string(payload))
	return err
}

// Provision creates or retrieves a Signet-custodied identity and returns its
// one-time bunker URI only to the caller for immediate protected-file handoff.
func (c *ContainerSignetctl) Provision(ctx context.Context, identityID string) (string, error) {
	if !safeAgentID(identityID) {
		return "", fmt.Errorf("invalid Signet identity id")
	}
	out, err := c.run(ctx, "provision", identityID)
	if err != nil {
		return "", err
	}
	bunkerURI, err := signetctlBunkerURI(out)
	for i := range out {
		out[i] = 0
	}
	if err != nil {
		return "", err
	}
	if _, _, _, err := sanitizeOneTimeBunkerURI(bunkerURI); err != nil {
		return "", fmt.Errorf("Signet provision returned an invalid one-time bunker URI: %w", err)
	}
	return bunkerURI, nil
}

func (c *ContainerSignetctl) RevokeClient(ctx context.Context, clientPubkey string) error {
	if !isHexPubkey(clientPubkey) {
		return fmt.Errorf("exact NIP-46 client pubkey is required")
	}
	_, err := c.run(ctx, "revoke-client", strings.ToLower(clientPubkey))
	return err
}

func (c *ContainerSignetctl) run(ctx context.Context, args ...string) ([]byte, error) {
	credential, err := readProtectedFile(c.config.ProvisionerCredentialFile, c.config.CredentialOwnerUID)
	if err != nil {
		return nil, fmt.Errorf("read provisioner credential at execution time: %w", err)
	}
	defer func() {
		for i := range credential {
			credential[i] = 0
		}
	}()
	credential = bytes.TrimSpace(credential)
	if len(credential) == 0 || bytes.IndexByte(credential, '\n') >= 0 {
		return nil, fmt.Errorf("provisioner credential file is empty or malformed")
	}
	// The credential travels only on stdin to a fixed container shell. It is
	// neither copied into the container filesystem nor exposed in argv/env of
	// the host-side docker process.
	script := `IFS= read -r SIGNET_PROVISIONER_NSEC || exit 64; export SIGNET_PROVISIONER_NSEC; exec signetctl "$@"`
	dockerArgs := []string{"exec", "-i", c.config.Container, "sh", "-c", script, "signetctl", "-c", c.config.ConfigPath}
	dockerArgs = append(dockerArgs, args...)
	stdin := append(append([]byte(nil), credential...), '\n')
	out, err := c.config.Runner.Run(ctx, c.config.DockerBin, dockerArgs, stdin)
	if err != nil {
		return nil, err
	}
	if err := validateSignetctlResponse(out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateSignetctlResponse(output []byte) error {
	start := bytes.LastIndex(output, []byte("\n{"))
	if start >= 0 {
		start++
	} else {
		start = bytes.IndexByte(output, '{')
	}
	if start < 0 {
		return fmt.Errorf("signetctl returned no JSON-RPC response")
	}
	var response struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output[start:]), &response); err != nil {
		return fmt.Errorf("parse signetctl JSON-RPC response: %w", err)
	}
	// Correlation is checked by signetctl against the locally generated request
	// ID before it prints a response. Bahia still checks that a correlated ID is
	// present before looking at result/error, preserving request-ID-first order.
	if len(bytes.TrimSpace(response.ID)) == 0 || bytes.Equal(bytes.TrimSpace(response.ID), []byte("null")) {
		return fmt.Errorf("signetctl JSON-RPC response is missing a correlated request id")
	}
	if len(bytes.TrimSpace(response.Error)) > 0 && !bytes.Equal(bytes.TrimSpace(response.Error), []byte("null")) {
		return fmt.Errorf("signetctl JSON-RPC response reported an error")
	}
	if len(response.Result) > 0 {
		var result interface{}
		if json.Unmarshal(response.Result, &result) == nil && responseContainsCode(result, "policy_set_not_persisted") {
			return fmt.Errorf("signetctl policy update was not durably persisted")
		}
	}
	return nil
}

func signetctlBunkerURI(output []byte) (string, error) {
	start := bytes.LastIndex(output, []byte("\n{"))
	if start >= 0 {
		start++
	} else {
		start = bytes.IndexByte(output, '{')
	}
	if start < 0 {
		return "", fmt.Errorf("signetctl provision returned no JSON-RPC response")
	}
	var response interface{}
	if err := json.Unmarshal(bytes.TrimSpace(output[start:]), &response); err != nil {
		return "", fmt.Errorf("parse signetctl provision response: %w", err)
	}
	if bunkerURI := findStringField(response, "bunker_uri"); bunkerURI != "" {
		return bunkerURI, nil
	}
	return "", fmt.Errorf("signetctl provision response omitted bunker_uri")
}

func findStringField(value interface{}, key string) string {
	switch typed := value.(type) {
	case map[string]interface{}:
		if found, ok := typed[key].(string); ok {
			return strings.TrimSpace(found)
		}
		for _, child := range typed {
			if found := findStringField(child, key); found != "" {
				return found
			}
		}
	case []interface{}:
		for _, child := range typed {
			if found := findStringField(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func responseContainsCode(value interface{}, code string) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		if typedCode, ok := typed["code"].(string); ok && typedCode == code {
			return true
		}
		for _, child := range typed {
			if responseContainsCode(child, code) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if responseContainsCode(child, code) {
				return true
			}
		}
	}
	return false
}

func sanitizeOneTimeBunkerURI(raw string) (string, string, []string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "bunker" || !isHexPubkey(parsed.Host) {
		return "", "", nil, fmt.Errorf("invalid one-time bunker URI")
	}
	relays := parsed.Query()["relay"]
	if len(relays) == 0 || parsed.Query().Get("secret") == "" {
		return "", "", nil, fmt.Errorf("one-time bunker URI requires relay and secret")
	}
	for _, relay := range relays {
		u, err := url.Parse(relay)
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
			return "", "", nil, fmt.Errorf("invalid bunker relay")
		}
	}
	query := url.Values{}
	for _, relay := range uniqueSortedStrings(relays) {
		query.Add("relay", relay)
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String(), strings.ToLower(parsed.Host), uniqueSortedStrings(relays), nil
}

func validateEnrollmentRequest(req OpenClawSignetEnrollmentRequest) error {
	if !safeAgentID(req.AgentID) {
		return fmt.Errorf("invalid agent id")
	}
	for name, value := range map[string]string{"controller": req.ControllerPubkey, "runtime": req.RuntimePubkey, "managed": req.ManagedPubkey, "provisioner": req.ProvisionerPubkey} {
		if !isHexPubkey(value) {
			return fmt.Errorf("%s pubkey must be 64 hex characters", name)
		}
	}
	return nil
}

func matchEnrollmentIdentity(state OpenClawSignetIdentityContract, req OpenClawSignetEnrollmentRequest) error {
	if state.ManagedPubkey != strings.ToLower(req.ManagedPubkey) || state.ControllerPubkey != strings.ToLower(req.ControllerPubkey) || state.RuntimePubkey != strings.ToLower(req.RuntimePubkey) || state.ProvisionerPubkey != strings.ToLower(req.ProvisionerPubkey) {
		return fmt.Errorf("existing OpenClaw Signet enrollment conflicts with requested identity contract")
	}
	return nil
}

func readProtectedFile(path string, ownerUID int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("protected file must be regular and mode 0600 or stricter")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != ownerUID {
		return nil, fmt.Errorf("protected file has unexpected owner")
	}
	return os.ReadFile(path)
}

func writeProtectedJSON(path string, value interface{}, ownerUID int) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeProtectedFile(path, append(data, '\n'), ownerUID)
}

func writeProtectedFile(path string, data []byte, ownerUID int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".protected-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if ownerUID != os.Geteuid() {
		if err := os.Chown(tmpPath, ownerUID, -1); err != nil {
			return err
		}
	}
	return os.Rename(tmpPath, path)
}

func normalizedKinds(kinds []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(kinds))
	for _, kind := range kinds {
		if kind < 0 || kind > 65535 {
			continue
		}
		if _, ok := seen[kind]; !ok {
			seen[kind] = struct{}{}
			out = append(out, kind)
		}
	}
	sort.Ints(out)
	return out
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "*" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func safeAgentID(value string) bool {
	if value == "" || len(value) > 127 || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !valid {
			return false
		}
	}
	return true
}

func isHexPubkey(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func clientPathClean(path string) string { return filepath.Clean(strings.TrimSpace(path)) }
