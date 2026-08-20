package soulfactory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/domain"
)

type recordingSignetPolicyAdmin struct {
	policies  []SignetClientPolicy
	agents    []string
	revoked   []string
	setErr    error
	revokeErr error
}

func (a *recordingSignetPolicyAdmin) SetPolicy(_ context.Context, agent string, policy SignetClientPolicy) error {
	a.agents = append(a.agents, agent)
	a.policies = append(a.policies, policy)
	return a.setErr
}

func (a *recordingSignetPolicyAdmin) RevokeClient(_ context.Context, client string) error {
	a.revoked = append(a.revoked, client)
	return a.revokeErr
}

type recordingConnectivityVerifier struct {
	calls []verifyCall
	err   error
}

type verifyCall struct {
	bunker string
	key    string
	pubkey string
	kinds  []int
}

func (v *recordingConnectivityVerifier) Verify(_ context.Context, bunker, key, pubkey string, kinds []int) error {
	v.calls = append(v.calls, verifyCall{bunker: bunker, key: key, pubkey: pubkey, kinds: append([]int(nil), kinds...)})
	return v.err
}

func TestOpenClawSignetEnrollmentIsRestartSafeAndExistingIdentityIsIdempotent(t *testing.T) {
	root := t.TempDir()
	admin := &recordingSignetPolicyAdmin{}
	verifier := &recordingConnectivityVerifier{}
	manager := newEnrollmentManagerForTest(t, root, admin, verifier)
	req := enrollmentRequestForTest()
	oneTimeURI := req.BunkerURI
	if err := manager.StageHandoff(t.Context(), req.AgentID, oneTimeURI); err != nil {
		t.Fatalf("StageHandoff: %v", err)
	}
	handoff, clientKey, state := manager.paths(req.AgentID)
	if info, err := os.Stat(handoff); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("transient handoff protection: info=%v err=%v", info, err)
	}
	req.BunkerURI = ""

	contract, err := manager.Enroll(t.Context(), req)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if contract.ManagedPubkey != req.ManagedPubkey || contract.ProvisionerPubkey != req.ProvisionerPubkey {
		t.Fatalf("identity contract = %+v", contract)
	}
	if strings.Contains(contract.BunkerURL, "secret") || strings.Contains(contract.BunkerURL, "one-time") {
		t.Fatalf("durable bunker URL retained one-time secret: %q", contract.BunkerURL)
	}
	if len(admin.policies) != 1 || admin.policies[0].ClientPubkey != contract.ClientPubkey {
		t.Fatalf("policy was not bound to exact client: %+v", admin.policies)
	}
	if containsString(admin.policies[0].Methods, "*") {
		t.Fatalf("policy methods contain wildcard: %+v", admin.policies[0].Methods)
	}
	for _, required := range []int{cascadia.NIP59_GIFT_WRAP, cascadia.CAS_AUDIT, cascadia.CAS_INTENT, cascadia.CAS_AGENT_HEARTBEAT, cascadia.CAS_AGENT_CAPABILITY} {
		if !containsInt(admin.policies[0].EventKinds, required) {
			t.Fatalf("policy kinds %v missing Cascadia runtime kind %d", admin.policies[0].EventKinds, required)
		}
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("one-time handoff still exists after durable proof: %v", err)
	}
	for _, path := range []string{clientKey, state} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 0600", path, info.Mode().Perm())
		}
	}
	stateBytes, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateBytes, []byte("one-time")) {
		t.Fatalf("durable state leaked one-time bunker secret: %s", stateBytes)
	}
	keyBefore, err := os.ReadFile(clientKey)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a Bahia restart. Exact replay has no one-time URI and must use
	// the same durable client key/binding instead of minting another one.
	restarted := newEnrollmentManagerForTest(t, root, admin, verifier)
	req.BunkerURI = ""
	replayed, err := restarted.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile after restart: %v", err)
	}
	keyAfter, err := os.ReadFile(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyBefore, keyAfter) || replayed.ClientPubkey != contract.ClientPubkey {
		t.Fatal("restart reconciliation replaced the managed NIP-46 client key")
	}
	if len(verifier.calls) != 2 || strings.Contains(verifier.calls[1].bunker, "secret") {
		t.Fatalf("restart verifier calls = %+v", verifier.calls)
	}
	if len(admin.policies) != 2 || !reflect.DeepEqual(admin.policies[0], admin.policies[1]) {
		t.Fatalf("reconciled policy changed: %+v", admin.policies)
	}

	// A retry after durable-state persistence but before transient deletion
	// must remove the leftover handoff and reconcile live policy in place.
	if err := os.WriteFile(handoff, []byte(oneTimeURI+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req.AllowedKinds = append(req.AllowedKinds, 7)
	reconciled, err := restarted.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile changed policy: %v", err)
	}
	if !containsInt(reconciled.EventKinds, 7) || len(admin.policies) != 3 || !containsInt(admin.policies[2].EventKinds, 7) {
		t.Fatalf("changed policy was not reconciled: contract=%v policies=%+v", reconciled.EventKinds, admin.policies)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("leftover consumed handoff was not deleted: %v", err)
	}
}

func TestOpenClawSignetEnrollmentFailureCompensatesAndPreservesTransientHandoff(t *testing.T) {
	root := t.TempDir()
	admin := &recordingSignetPolicyAdmin{}
	verifier := &recordingConnectivityVerifier{err: errors.New("bunker denied sign_event")}
	manager := newEnrollmentManagerForTest(t, root, admin, verifier)
	req := enrollmentRequestForTest()

	contract, err := manager.Enroll(t.Context(), req)
	if err == nil || contract != nil || !strings.Contains(err.Error(), "bunker denied") {
		t.Fatalf("Enroll error = %v, contract=%+v", err, contract)
	}
	if len(admin.revoked) != 1 || admin.revoked[0] != admin.policies[0].ClientPubkey {
		t.Fatalf("compensating revokes = %v, policies=%+v", admin.revoked, admin.policies)
	}
	handoff, clientKey, state := manager.paths(req.AgentID)
	if _, err := os.Stat(handoff); err != nil {
		t.Fatalf("unproven one-time handoff was deleted: %v", err)
	}
	if _, err := os.Stat(clientKey); err != nil {
		t.Fatalf("retry-stable client key was deleted: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("failed enrollment wrote durable success state: %v", err)
	}
}

func TestOpenClawSignetEnrollmentPolicyDenialStopsBeforeConnectivity(t *testing.T) {
	admin := &recordingSignetPolicyAdmin{setErr: errors.New("policy denied")}
	verifier := &recordingConnectivityVerifier{}
	manager := newEnrollmentManagerForTest(t, t.TempDir(), admin, verifier)
	if _, err := manager.Enroll(t.Context(), enrollmentRequestForTest()); err == nil || !strings.Contains(err.Error(), "policy denied") {
		t.Fatalf("Enroll error = %v", err)
	}
	if len(verifier.calls) != 0 || len(admin.revoked) != 0 {
		t.Fatalf("policy denial continued to connectivity/cleanup: verifier=%d revoked=%v", len(verifier.calls), admin.revoked)
	}
}

func TestOpenClawSignetEnrollmentRevokeRemovesBindingAndFiles(t *testing.T) {
	root := t.TempDir()
	admin := &recordingSignetPolicyAdmin{}
	manager := newEnrollmentManagerForTest(t, root, admin, &recordingConnectivityVerifier{})
	req := enrollmentRequestForTest()
	contract, err := manager.Enroll(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(t.Context(), req.AgentID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !reflect.DeepEqual(admin.revoked, []string{contract.ClientPubkey}) {
		t.Fatalf("revoked clients = %v", admin.revoked)
	}
	for _, path := range []string{contract.ClientKeyRef, filepath.Join(root, "state", req.AgentID+".json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("revoked material remains at %s: %v", path, err)
		}
	}
}

func TestMetiqSignetEnrollmentUsesSigningOnlyExactClientProfile(t *testing.T) {
	root := t.TempDir()
	admin := &recordingSignetPolicyAdmin{}
	verifier := &recordingConnectivityVerifier{}
	profile := MetiqRuntimeSignetEnrollmentProfile()
	manager, err := NewOpenClawSignetEnrollmentManager(OpenClawSignetEnrollmentConfig{
		StateDir: filepath.Join(root, "state"), ClientKeyDir: filepath.Join(root, "keys"),
		FileOwnerUID: os.Geteuid(), PolicyAdmin: admin, Verifier: verifier, Profile: &profile,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := enrollmentRequestForTest()
	req.AgentID = "metiq-runtime"
	req.RuntimePubkey = req.ManagedPubkey
	req.AllowedKinds = nil
	contract, err := manager.Enroll(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Schema != RuntimeSignetIdentityContractSchema || len(admin.policies) != 1 || admin.policies[0].ClientPubkey != contract.ClientPubkey {
		t.Fatalf("contract=%+v policies=%+v", contract, admin.policies)
	}
	wantMethods := []string{"connect", "get_public_key", "get_relays", "ping", "sign_event"}
	if !reflect.DeepEqual(admin.policies[0].Methods, wantMethods) {
		t.Fatalf("methods=%v want=%v", admin.policies[0].Methods, wantMethods)
	}
	wantKinds := []int{cascadia.CAS_AGENT_CAPABILITY, domain.KindRuntimeControlResult}
	if !reflect.DeepEqual(admin.policies[0].EventKinds, wantKinds) {
		t.Fatalf("event kinds=%v want=%v", admin.policies[0].EventKinds, wantKinds)
	}
	if containsString(admin.policies[0].Methods, "nip44_encrypt") || containsString(admin.policies[0].Methods, "nip44_decrypt") || containsString(admin.policies[0].Methods, "*") {
		t.Fatalf("Metiq policy is broader than signing-only: %+v", admin.policies[0])
	}
}

type recordingSignetctlRunner struct {
	name  string
	args  []string
	stdin []byte
	out   []byte
}

func (r *recordingSignetctlRunner) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	r.stdin = append([]byte(nil), stdin...)
	if r.out != nil {
		return append([]byte(nil), r.out...), nil
	}
	return []byte(`{"jsonrpc":"2.0","id":"matched","result":{"ok":true}}`), nil
}

func TestContainerSignetctlReadsCredentialAtExecutionAndNeverPlacesItInArgv(t *testing.T) {
	credential := "nsec1host-only-provisioner"
	credentialFile := filepath.Join(t.TempDir(), "provisioner")
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingSignetctlRunner{}
	client, err := NewContainerSignetctl(SignetctlConfig{
		Container: "signetd", ConfigPath: "/data/signet.toml",
		ProvisionerCredentialFile: credentialFile, CredentialOwnerUID: os.Geteuid(), Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := SignetClientPolicy{ClientPubkey: strings.Repeat("a", 64), Methods: []string{"sign_event", "*", "nip44_decrypt"}, EventKinds: []int{cascadia.CAS_INTENT}}
	if err := client.SetPolicy(t.Context(), "agent-one", policy); err != nil {
		t.Fatal(err)
	}
	joinedArgs := strings.Join(append([]string{runner.name}, runner.args...), " ")
	if strings.Contains(joinedArgs, credential) {
		t.Fatalf("host provisioner credential leaked into argv: %s", joinedArgs)
	}
	if string(runner.stdin) != credential+"\n" {
		t.Fatalf("credential stdin was not read at execution: %q", runner.stdin)
	}
	if !strings.Contains(joinedArgs, "set-policy") || strings.Contains(joinedArgs, `\"*\"`) {
		t.Fatalf("signetctl policy argv = %s", joinedArgs)
	}
	var policyArg map[string]interface{}
	if err := json.Unmarshal([]byte(runner.args[len(runner.args)-1]), &policyArg); err != nil {
		t.Fatalf("parse policy arg: %v", err)
	}
	clients := policyArg["allow_clients"].([]interface{})
	if len(clients) != 1 || clients[0] != strings.Repeat("a", 64) {
		t.Fatalf("allow_clients = %#v", clients)
	}
}

func TestContainerSignetctlRejectsUnprotectedCredentialFile(t *testing.T) {
	credentialFile := filepath.Join(t.TempDir(), "provisioner")
	if err := os.WriteFile(credentialFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, err := NewContainerSignetctl(SignetctlConfig{Container: "signetd", ConfigPath: "/data/signet.toml", ProvisionerCredentialFile: credentialFile, CredentialOwnerUID: os.Geteuid(), Runner: &recordingSignetctlRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RevokeClient(t.Context(), strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("RevokeClient error = %v", err)
	}
}

func TestValidateSignetctlResponseChecksRequestIDBeforeResult(t *testing.T) {
	missingID := []byte(`{"jsonrpc":"2.0","error":{"message":"stale denial"}}`)
	if err := validateSignetctlResponse(missingID); err == nil || !strings.Contains(err.Error(), "request id") {
		t.Fatalf("missing-id response error = %v", err)
	}
	nonDurable := []byte("Published set_policy ContextVM intent\nReply received:\n" +
		`{"jsonrpc":"2.0","id":"current","result":{"response":{"code":"policy_set_not_persisted"}}}`)
	if err := validateSignetctlResponse(nonDurable); err == nil || !strings.Contains(err.Error(), "not durably") {
		t.Fatalf("non-durable response error = %v", err)
	}
}

func TestContainerSignetctlProvisionReturnsOneTimeURIWithoutCredentialInArgv(t *testing.T) {
	credential := "nsec1host-only-provisioner"
	credentialFile := filepath.Join(t.TempDir(), "provisioner")
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bunkerURI := "bunker://" + strings.Repeat("5", 64) + "?relay=wss%3A%2F%2Frelay.example&secret=one-time"
	runner := &recordingSignetctlRunner{out: []byte(`{"jsonrpc":"2.0","id":"matched","result":{"bunker_uri":"` + bunkerURI + `"}}`)}
	client, err := NewContainerSignetctl(SignetctlConfig{
		Container: "signetd", ConfigPath: "/data/signet.toml",
		ProvisionerCredentialFile: credentialFile, CredentialOwnerUID: os.Geteuid(), Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Provision(t.Context(), "metiq-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if got != bunkerURI {
		t.Fatal("provision did not return the one-time handoff")
	}
	joinedArgs := strings.Join(append([]string{runner.name}, runner.args...), " ")
	if strings.Contains(joinedArgs, credential) || strings.Contains(joinedArgs, bunkerURI) {
		t.Fatal("credential or one-time handoff leaked into host argv")
	}
	if !strings.Contains(joinedArgs, "provision metiq-runtime") {
		t.Fatalf("signetctl argv = %s", joinedArgs)
	}
}

func newEnrollmentManagerForTest(t *testing.T, root string, admin SignetPolicyAdmin, verifier SignetConnectivityVerifier) *OpenClawSignetEnrollmentManager {
	t.Helper()
	manager, err := NewOpenClawSignetEnrollmentManager(OpenClawSignetEnrollmentConfig{
		StateDir: filepath.Join(root, "state"), ClientKeyDir: filepath.Join(root, "keys"),
		FileOwnerUID: os.Geteuid(), PolicyAdmin: admin, Verifier: verifier,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)),
	})
	if err != nil {
		t.Fatalf("NewOpenClawSignetEnrollmentManager: %v", err)
	}
	return manager
}

func enrollmentRequestForTest() OpenClawSignetEnrollmentRequest {
	return OpenClawSignetEnrollmentRequest{
		AgentID: "agent-one", ControllerPubkey: strings.Repeat("1", 64), RuntimePubkey: strings.Repeat("2", 64),
		ManagedPubkey: strings.Repeat("3", 64), ProvisionerPubkey: strings.Repeat("4", 64),
		BunkerURI:    "bunker://" + strings.Repeat("5", 64) + "?relay=wss%3A%2F%2Frelay.example&secret=one-time",
		AllowedKinds: []int{1, cascadia.CAS_INTENT},
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
