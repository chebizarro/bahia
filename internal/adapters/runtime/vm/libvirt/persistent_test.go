package libvirt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	native "github.com/digitalocean/go-libvirt"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func conformanceID(n string) uuid.UUID {
	return uuid.MustParse("00000000-0000-0000-0000-00000000000" + n)
}
func conformanceMarker() domain.VMOwnershipMarker {
	return domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: domain.VMResourceIdentity{InstallationID: conformanceID("1"), OrgID: conformanceID("2"), HostID: conformanceID("3"), DeploymentID: conformanceID("4"), Provider: domain.VMProviderLibvirt, ProviderResourceID: conformanceID("5"), LifecycleClass: domain.VMLifecyclePersistent}, AppliedGeneration: 1, OperationID: conformanceID("6"), ImageDigest: "sha256:" + strings.Repeat("a", 64), ConfigDigest: "sha256:" + strings.Repeat("b", 64)}
}

type conformanceHost struct {
	xml                 []byte
	state               string
	calls               [][]string
	registered          bool
	requireRegistration bool
}

func (h *conformanceHost) run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("unbounded provider command")
	}
	h.calls = append(h.calls, append([]string{filepath.Base(binary)}, args...))
	if filepath.Base(binary) == "qemu-img" {
		switch args[0] {
		case "create":
			return nil, os.WriteFile(args[len(args)-1], []byte("overlay"), 0600)
		case "info":
			return []byte(`{"virtual-size":4096}`), nil
		case "resize":
			return nil, nil
		}
		return nil, errors.New("unexpected qemu command")
	}
	op := args[2]
	switch op {
	case "list":
		if h.xml == nil {
			return nil, nil
		}
		return []byte(conformanceID("5").String()), nil
	case "dumpxml":
		return h.xml, nil
	case "domstate":
		return []byte(h.state), nil
	case "define":
		var err error
		h.xml, err = os.ReadFile(args[3])
		h.state = "shut off"
		return nil, err
	case "autostart":
		return nil, nil
	case "start", "shutdown", "reboot", "destroy":
		if h.requireRegistration && !h.registered {
			return nil, errors.New("mutation before registration")
		}
		if op == "shutdown" || op == "destroy" {
			h.state = "shut off"
		} else {
			h.state = "running"
		}
		return nil, nil
	case "undefine":
		h.xml = nil
		return nil, nil
	case "metadata":
		return nil, nil
	default:
		return nil, errors.New("unexpected virsh command " + op)
	}
}

type conformanceEvents struct {
	host              *conformanceHost
	event             DomainEvent
	err               error
	closed            bool
	registrationError bool
}

func (e *conformanceEvents) Subscribe(context.Context, uuid.UUID, bool) (DomainSubscription, error) {
	if e.registrationError {
		return nil, errors.New("registration rejected")
	}
	e.host.registered = true
	return e, nil
}
func (e *conformanceEvents) Next(context.Context) (DomainEvent, error) { return e.event, e.err }
func (e *conformanceEvents) Close() error                              { e.closed = true; return nil }

func TestLegacyMutationCannotReachAdoptedDomain(t *testing.T) {
	data, err := os.ReadFile("../testdata/conformance/owned-domain.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"start", "stop", "destroy"} {
		t.Run(operation, func(t *testing.T) {
			runner := &fakeRunner{responses: []fakeResponse{{match: "dumpxml", output: string(data)}}}
			driver, _ := newTestDriver(t, runner)
			var err error
			switch operation {
			case "start":
				err = driver.Start(context.Background(), "legacy-display-name")
			case "stop":
				err = driver.Stop(context.Background(), "legacy-display-name", false)
			case "destroy":
				err = driver.Destroy(context.Background(), "legacy-display-name")
			}
			var providerError *domain.VMProviderError
			if !errors.As(err, &providerError) || providerError.Code != domain.VMErrorForeign {
				t.Fatalf("legacy mutation reached adopted domain: %v", err)
			}
			for _, call := range runner.calls {
				if !strings.Contains(call, " dumpxml ") && !strings.Contains(call, " domstate ") {
					t.Fatalf("legacy path actuated marked resource: %v", runner.calls)
				}
			}
		})
	}
}

func TestConformanceDefineArgvAndOwnershipXML(t *testing.T) {
	root := t.TempDir()
	instances := filepath.Join(root, "instances")
	dir := filepath.Join(instances, conformanceID("5").String())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	image := t.TempDir()
	if err := os.WriteFile(filepath.Join(image, "disk.qcow2"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	h := &conformanceHost{}
	d := New(Config{InstancesDir: instances, ImageRoot: image, Runner: h.run}, nil)
	marker := conformanceMarker()
	spec := vm.PersistentSpec{Marker: marker, Deployment: domain.PersistentVMDeployment{Allocation: domain.VMCapacity{DiskBytes: 1 << 30}, Firmware: domain.VMFirmwareBIOS}, Instance: vm.InstanceSpec{Name: marker.ProviderResourceID.String(), InstanceDir: dir, VCPUs: 2, MemoryMB: 2048, Image: vm.ImageSpec{Format: vm.FormatQCOW2, Arch: "amd64", DiskPath: filepath.Join(image, "disk.qcow2")}}}
	if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
		t.Fatal(err)
	}
	var actual [][]string
	for _, call := range h.calls {
		if call[0] == "virsh" && call[3] == "list" {
			continue
		}
		normalized := make([]string, len(call))
		for i, arg := range call {
			normalized[i] = strings.ReplaceAll(strings.ReplaceAll(arg, dir, "$INSTANCE"), image, "$IMAGE")
		}
		actual = append(actual, normalized)
	}
	data, err := os.ReadFile("../testdata/conformance/libvirt-define.argv.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected [][]string
	if err = json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("argv transformation\ngot: %#v\nwant:%#v", actual, expected)
	}
	goldenXML, err := os.ReadFile("../testdata/conformance/owned-domain.xml")
	if err != nil {
		t.Fatal(err)
	}
	expectedXMLDigest, err := normalizedXMLDigest(goldenXML)
	if err != nil {
		t.Fatal(err)
	}
	actualXMLDigest, err := normalizedXMLDigest([]byte(strings.ReplaceAll(string(h.xml), dir, "$INSTANCE")))
	if err != nil || actualXMLDigest != expectedXMLDigest {
		t.Fatalf("domain XML does not match conformance fixture: %s / %v", h.xml, err)
	}
	observed, err := d.InspectPersistent(context.Background(), marker.ProviderResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Marker == nil || !reflect.DeepEqual(*observed.Marker, marker) || observed.State != domain.VMRuntimeStopped || observed.Components[domain.VMComponentDisk] != filepath.Join(dir, "disk.qcow2") {
		t.Fatalf("XML transformation: %+v", observed)
	}
	if !strings.Contains(string(h.xml), "<uuid>"+marker.ProviderResourceID.String()+"</uuid>") || strings.Contains(string(h.xml), "<tpm") {
		t.Fatalf("unexpected XML: %s", h.xml)
	}
}
func TestConformanceEventRegistrationAndStreamLoss(t *testing.T) {
	for _, tt := range []struct {
		name           string
		lost, rejected bool
	}{{"confirmed", false, false}, {"lost_mid_operation", true, false}, {"registration_rejected", false, true}} {
		t.Run(tt.name, func(t *testing.T) {
			marker := conformanceMarker()
			doc, err := domainXML(domainParams{Name: "display-name-not-identity", MemoryMB: 2048, VCPUs: 2, Overlay: "/private/disk", ConsoleLog: "/private/log", Marker: &marker})
			if err != nil {
				t.Fatal(err)
			}
			h := &conformanceHost{xml: doc, state: "running", requireRegistration: true}
			events := &conformanceEvents{host: h, event: DomainEvent{ID: marker.ProviderResourceID, State: domain.VMRuntimeStopped}, registrationError: tt.rejected}
			if tt.lost {
				events.err = errors.New("event stream lost")
			}
			d := New(Config{InstancesDir: t.TempDir(), Runner: h.run, Events: events}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			before, err := d.InspectPersistent(ctx, marker.ProviderResourceID)
			if err != nil {
				t.Fatal(err)
			}
			err = d.TransitionPersistent(ctx, before, domain.VMOperationGracefulStop, false)
			if tt.lost {
				var pe *domain.VMProviderError
				if !errors.As(err, &pe) || !pe.Unconfirmed || h.state != "shut off" {
					t.Fatalf("lost stream reported success: %v", err)
				}
			} else if tt.rejected {
				if err == nil || h.state != "running" {
					t.Fatalf("registration failure mutated: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !tt.rejected && !events.closed {
				t.Fatal("subscription leaked")
			}
		})
	}
}
func TestConformanceFingerprintRecheckRejectsAdoptionRace(t *testing.T) {
	marker := conformanceMarker()
	doc, _ := domainXML(domainParams{Name: "foreign", MemoryMB: 2048, VCPUs: 2, Overlay: "/private/disk", ConsoleLog: "/private/log"})
	h := &conformanceHost{xml: doc, state: "shut off"}
	// The UUID is explicit even for an unowned domain, not inferred from its name.
	h.xml = []byte(strings.Replace(string(h.xml), "<name>foreign</name>", "<name>foreign</name><uuid>"+marker.ProviderResourceID.String()+"</uuid>", 1))
	d := New(Config{InstancesDir: t.TempDir(), Runner: h.run}, nil)
	r, err := d.InspectPersistent(context.Background(), marker.ProviderResourceID)
	if err != nil {
		t.Fatal(err)
	}
	h.xml = []byte(strings.Replace(string(h.xml), "2048", "4096", 1))
	before := len(h.calls)
	if err = d.AdoptPersistent(context.Background(), r, marker); err == nil {
		t.Fatal("adopted changed provider definition")
	}
	for _, call := range h.calls[before:] {
		if call[3] == "metadata" {
			t.Fatal("ownership changed after fingerprint race")
		}
	}
}
func TestConformanceNativeEventUUIDMapping(t *testing.T) {
	id := conformanceID("5")
	event, ok := nativeDomainEvent(&native.DomainEventCallbackLifecycleMsg{Msg: native.DomainEventLifecycleMsg{Dom: native.Domain{UUID: native.UUID(id)}, Event: int32(native.DomainEventStopped)}})
	if !ok || event.ID != id || event.State != domain.VMRuntimeStopped {
		t.Fatalf("event transform %+v %v", event, ok)
	}
	event, ok = nativeDomainEvent(&native.DomainEventCallbackRebootMsg{Msg: native.DomainEventRebootMsg{Dom: native.Domain{UUID: native.UUID(id)}}})
	if !ok || !event.Reboot || event.ID != id {
		t.Fatal("reboot mapping")
	}
}
func TestBoundedBackendRejectsShellAndOutputOverflow(t *testing.T) {
	if _, err := execRunner(context.Background(), "/bin/sh", "-c", "true"); err == nil {
		t.Fatal("shell accepted")
	}
	if _, err := execRunner(context.Background(), "virsh", "list"); err == nil {
		t.Fatal("PATH lookup accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &boundedOutput{cancel: cancel}
	if _, err := out.Write(make([]byte, maxCommandOutput+1)); err == nil || ctx.Err() == nil {
		t.Fatal("overflow not bounded/canceled")
	}
}
