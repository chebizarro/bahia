package vm_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm/firecracker"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm/libvirt"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type adoptionHost struct {
	id            uuid.UUID
	xml           []byte
	disk, base    string
	mutations     int
	foreignBase   bool
	guestMismatch bool
	standalone    bool
	externalData  bool
	lostWatch     bool
	beforeMarker  func()
}

func (h *adoptionHost) run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, fmt.Errorf("unbounded command")
	}
	if filepath.Base(binary) == "qemu-img" {
		if args[0] == "compare" {
			if h.guestMismatch {
				return nil, fmt.Errorf("disk contents differ")
			}
			return nil, nil
		}
		if strings.Join(args[:3], " ") != "info --backing-chain --output=json" {
			return nil, fmt.Errorf("unexpected image mutation")
		}
		if h.standalone {
			return json.Marshal([]map[string]any{{"filename": args[len(args)-1], "format": "qcow2", "virtual-size": 4096}})
		}
		if h.externalData {
			return []byte(`[{"format-specific":{"data":{"data-file":"unverified"}}}]`), nil
		}
		base := h.base
		if h.foreignBase {
			base += ".foreign"
		}
		return json.Marshal([]map[string]any{{"filename": h.disk, "format": "qcow2", "virtual-size": 4096, "full-backing-filename": base, "backing-filename": base, "backing-filename-format": "qcow2"}, {"filename": base, "format": "qcow2", "virtual-size": 4096}})
	}
	switch args[2] {
	case "list":
		return []byte(h.id.String()), nil
	case "dumpxml":
		return h.xml, nil
	case "domstate":
		return []byte("shut off"), nil
	case "dominfo":
		return []byte("Persistent: yes\nAutostart: disable\n"), nil
	case "metadata":
		if h.beforeMarker != nil {
			h.beforeMarker()
		}
		h.mutations++
		for i, a := range args {
			if a == "--set" {
				start, end := strings.Index(string(h.xml), "<metadata>"), strings.Index(string(h.xml), "</metadata>")
				contents := string(h.xml[start+len("<metadata>") : end])
				if own := strings.Index(contents, `<ownership xmlns="urn:bahia:persistent-vm:2">`); own >= 0 {
					endOwn := strings.Index(contents[own:], "</ownership>") + own + len("</ownership>")
					contents = contents[:own] + contents[endOwn:]
				}
				h.xml = []byte(string(h.xml[:start]) + "<metadata>" + contents + args[i+1] + string(h.xml[end:]))
				return nil, nil
			}
		}
	}
	return nil, fmt.Errorf("unexpected command: %v", args)
}

type adoptionEvents struct{ host *adoptionHost }
type adoptionSubscription struct{ host *adoptionHost }

func (e adoptionEvents) Subscribe(context.Context, uuid.UUID, bool) (libvirt.DomainSubscription, error) {
	return adoptionSubscription{host: e.host}, nil
}
func (adoptionSubscription) Next(ctx context.Context) (libvirt.DomainEvent, error) {
	<-ctx.Done()
	return libvirt.DomainEvent{}, ctx.Err()
}
func (adoptionSubscription) Close() error { return nil }
func (s adoptionSubscription) CheckCold(ctx context.Context) error {
	if s.host.lostWatch {
		return fmt.Errorf("lost lifecycle watch")
	}
	return ctx.Err()
}

type adoptionFixture struct {
	p                       *vm.PersistentProvider
	q                       domain.VMProviderOperation
	dir, disk, base, config string
	firmware                string
	host                    *adoptionHost
	driver                  vm.PersistentDriver
}

func adoptionSetup(t *testing.T, provider domain.VMProvider) adoptionFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	meta := func(id, org uuid.UUID) domain.VirtualizationResourceMeta {
		return domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: id, OrgID: org, Generation: 1, CreatedBy: "operator"}
	}
	id := domain.VMResourceIdentity{InstallationID: uuid.New(), OrgID: uuid.New(), HostID: uuid.New(), DeploymentID: uuid.New(), Provider: provider, ProviderResourceID: uuid.New(), LifecycleClass: domain.VMLifecyclePersistent}
	h := domain.VirtualizationHost{VirtualizationResourceMeta: meta(id.HostID, id.OrgID), InstallationID: id.InstallationID, Provider: provider, Architecture: "amd64", ExecutionLocation: domain.VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, Capacity: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, Quota: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, OperationLimits: domain.DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 60}
	i := domain.VMImage{VirtualizationResourceMeta: meta(uuid.New(), id.OrgID), ReleaseRef: uuid.New(), Architecture: "amd64", OS: domain.VMOSLinux, LifecycleClasses: h.LifecycleClasses, DriverContract: "virtio-v1", AgentProtocolVersion: "2", Provenance: domain.VMProvenance{EventID: strings.Repeat("a", 64), Signer: strings.Repeat("b", 64), Verified: true, VerifiedAt: time.Now()}}
	i.Format, i.Firmware = domain.VMImageQCOW2, domain.VMFirmwareBIOS
	if provider == domain.VMProviderFirecracker {
		i.Format, i.Firmware = domain.VMImageFirecrackerRootFS, domain.VMFirmwareNone
	}
	v := domain.PersistentVMDeployment{VirtualizationResourceMeta: meta(id.DeploymentID, id.OrgID), Identity: id, HostID: h.ID, Provider: provider, ImageID: i.ID, LifecycleClass: id.LifecycleClass, Purpose: domain.VMPurposeService, DisplayName: "explicit enrollment", DesiredPower: domain.VMDesiredStopped, Allocation: domain.VMCapacity{VCPU: 2, MemoryBytes: 2 << 30, DiskBytes: 4096}, Firmware: i.Firmware, StoragePoolRef: uuid.New(), Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}, ConfigDigest: vm.DigestBytes([]byte("not authority"))}
	dir := filepath.Join(root, "instances", id.ProviderResourceID.String())
	images := filepath.Join(root, "images")
	release := filepath.Join(images, i.ReleaseRef.String())
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.MkdirAll(release, 0700))
	manifest := vm.Manifest{ImageID: "enrollment-snapshot", Arch: "amd64", Format: string(i.Format), SHA256: map[string]string{}}
	files := map[string]domain.VMComponentKind{"disk.qcow2": domain.VMComponentDisk}
	if provider == domain.VMProviderFirecracker {
		files = map[string]domain.VMComponentKind{"kernel": domain.VMComponentKernel, "rootfs.ext4": domain.VMComponentRootFS}
	}
	for name, kind := range files {
		data := []byte("verified " + name)
		if kind == domain.VMComponentRootFS {
			data = make([]byte, 4096)
			copy(data, "trusted raw snapshot")
		}
		require.NoError(t, os.WriteFile(filepath.Join(release, name), data, 0600))
		key := string(kind)
		if kind == domain.VMComponentDisk {
			key = "disk"
		}
		manifest.SHA256[key] = strings.TrimPrefix(vm.DigestBytes(data), "sha256:")
		i.Components = append(i.Components, domain.VMComponent{Kind: kind, StorageRef: uuid.New(), Digest: vm.DigestBytes(data), SizeBytes: int64(len(data))})
	}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(release, "manifest.json"), data, 0600))
	i.ManifestDigest = vm.DigestBytes(data)
	f := adoptionFixture{dir: dir, base: filepath.Join(release, "disk.qcow2"), firmware: filepath.Join(root, "firmware-code.fd")}
	require.NoError(t, os.WriteFile(f.firmware, []byte("configured firmware"), 0600))
	var driver vm.PersistentDriver
	if provider == domain.VMProviderLibvirt {
		f.disk = filepath.Join(dir, "disk.qcow2")
		require.NoError(t, os.WriteFile(f.disk, []byte("measured writable overlay"), 0600))
		data, err := os.ReadFile("testdata/conformance/owned-domain.xml")
		require.NoError(t, err)
		text := strings.ReplaceAll(string(data), "$INSTANCE", dir)
		text = strings.ReplaceAll(text, "00000000-0000-0000-0000-000000000005", id.ProviderResourceID.String())
		start, end := strings.Index(text, "<metadata>"), strings.Index(text, "</metadata>")
		text = text[:start] + "<metadata>" + text[end:]
		f.host = &adoptionHost{id: id.ProviderResourceID, xml: []byte(text), disk: f.disk, base: f.base}
		driver = libvirt.New(libvirt.Config{InstancesDir: filepath.Join(root, "instances"), ImageRoot: images, FirmwareCodePath: f.firmware, Runner: f.host.run, Events: adoptionEvents{host: f.host}}, nil)
	} else {
		f.disk, f.base, f.config = filepath.Join(dir, "rootfs.ext4"), filepath.Join(release, "rootfs.ext4"), filepath.Join(dir, "vmconfig.json")
		require.NoError(t, vm.CopyRegularFile(ctx, f.base, f.disk))
		data, err := os.ReadFile("testdata/conformance/firecracker-config.json")
		require.NoError(t, err)
		text := strings.ReplaceAll(strings.ReplaceAll(string(data), "$IMAGE", release), "$INSTANCE", dir)
		text = strings.ReplaceAll(text, `"mem_size_mib":512`, `"mem_size_mib":2048`)
		require.NoError(t, os.WriteFile(f.config, []byte(text), 0600))
		driver = firecracker.New(firecracker.Config{InstancesDir: filepath.Join(root, "instances"), ImageRoot: images}, nil)
	}
	f.p, err = vm.NewPersistentProvider(vm.PersistentConfig{Host: h, StoragePoolRef: v.StoragePoolRef, StateDir: root, VerifyImage: func(context.Context, domain.VirtualizationHost, domain.VMImage) error { return nil }, ResolveRelease: func(ctx context.Context, image domain.VMImage) (*vm.Release, error) {
		return vm.ResolvePinnedRelease(ctx, images, image)
	}}, driver)
	require.NoError(t, err)
	f.driver = driver
	approval := uuid.New()
	f.q = domain.VMProviderOperation{Host: h, Deployment: v, Image: i, Operation: domain.VMOperation{VirtualizationResourceMeta: meta(uuid.New(), id.OrgID), LifecycleClass: id.LifecycleClass, ResourceID: v.ID, ResourceGeneration: 1, ExpectedGeneration: 1, IdempotencyKey: "adopt", RequestHash: vm.DigestBytes([]byte("request")), Actor: "requester", Reason: "approved measured enrollment", Kind: domain.VMOperationAdopt, Phase: domain.VMOperationExecuting, RequiredTier: domain.VMApprovalDestructive, ApprovalID: &approval, ProviderCorrelationID: uuid.New(), Deadline: time.Now().Add(time.Minute)}}
	return f
}

func (f *adoptionFixture) measure(t *testing.T) {
	t.Helper()
	m, err := f.p.MeasureAdoption(context.Background(), domain.VMChangeRequest{Host: f.q.Host, Desired: f.q.Deployment, Image: f.q.Image})
	require.NoError(t, err)
	f.q.Operation.Adoption = m
	f.q.Operation.ProviderFingerprint = m.ProviderFingerprint
	f.q.Deployment.ConfigDigest = m.ConfigDigest
}

func TestMeasuredAdoptionConformance(t *testing.T) {
	for _, provider := range []domain.VMProvider{domain.VMProviderLibvirt, domain.VMProviderFirecracker} {
		for _, prior := range []string{"unmarked", "legacy-v1", "pre-inventory"} {
			t.Run(string(provider)+"/"+prior, func(t *testing.T) {
				f := adoptionSetup(t, provider)
				if f.host != nil {
					id := f.q.Deployment.Identity.ProviderResourceID.String()
					f.host.xml = []byte(strings.Replace(string(f.host.xml), "<name>"+id+"</name>", "<name>arbitrary-display-name</name>", 1))
				}
				if prior == "legacy-v1" {
					require.NoError(t, os.WriteFile(filepath.Join(f.dir, "metadata.json"), []byte(`{"image_digest":"caller claim","name":"bahia-legacy","schema_version":1}`), 0600))
				}
				if prior == "legacy-v1" && f.host != nil {
					f.host.xml = []byte(strings.Replace(string(f.host.xml), "<metadata></metadata>", `<metadata><ownership xmlns="urn:bahia:legacy-vm:1">`+f.q.Deployment.Identity.ProviderResourceID.String()+`</ownership></metadata>`, 1))
				}
				f.measure(t)
				if prior == "pre-inventory" {
					marker := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: f.q.Deployment.Identity, AppliedGeneration: 1, OperationID: uuid.New(), ImageDigest: f.q.Image.ManifestDigest, ConfigDigest: f.q.Deployment.ConfigDigest}
					data, err := json.Marshal(marker)
					require.NoError(t, err)
					if f.host != nil {
						var escaped strings.Builder
						require.NoError(t, xml.EscapeText(&escaped, data))
						f.host.xml = []byte(strings.Replace(string(f.host.xml), "<metadata></metadata>", `<metadata><ownership xmlns="urn:bahia:persistent-vm:2">`+escaped.String()+`</ownership></metadata>`, 1))
					} else {
						require.NoError(t, os.WriteFile(filepath.Join(f.dir, "ownership.json"), data, 0600))
					}
					record, err := json.Marshal(map[string]any{"schema_version": 2, "marker": marker, "deployment": f.q.Deployment, "fingerprint": f.q.Operation.ProviderFingerprint})
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(f.dir, "metadata.json"), record, 0600))
				}
				result, err := f.p.Execute(context.Background(), f.q)
				require.NoError(t, err)
				require.True(t, result.Confirmed)
				require.Equal(t, domain.VMOwned, result.Observation.Ownership)
				data, err := os.ReadFile(filepath.Join(f.dir, "metadata.json"))
				require.NoError(t, err)
				var record struct {
					Adoption   *domain.VMAdoptionMeasurement     `json:"adoption"`
					Components map[domain.VMComponentKind]string `json:"components"`
				}
				require.NoError(t, json.Unmarshal(data, &record))
				require.Equal(t, f.q.Operation.Adoption, record.Adoption)
				require.NotEmpty(t, record.Components)
				result, err = f.p.Execute(context.Background(), f.q)
				require.NoError(t, err)
				require.True(t, result.Confirmed)
				if f.host != nil {
					require.Equal(t, 1, f.host.mutations)
				}
			})
		}
	}
}

func TestMeasuredAdoptionRejectsChangesBeforeOwnership(t *testing.T) {
	for _, provider := range []domain.VMProvider{domain.VMProviderLibvirt, domain.VMProviderFirecracker} {
		for _, change := range []string{"config", "image", "writable", "inode", "foreign-owner", "unbound", "caller-pin", "lineage", "guest-content", "cold-watch", "external-data"} {
			t.Run(string(provider)+"/"+change, func(t *testing.T) {
				f := adoptionSetup(t, provider)
				f.measure(t)
				switch change {
				case "guest-content":
					if f.host != nil {
						f.host.guestMismatch = true
					} else {
						require.NoError(t, os.WriteFile(f.disk, make([]byte, 4096), 0600))
					}
				case "external-data":
					if f.host != nil {
						f.host.externalData = true
					} else {
						b, err := os.ReadFile(f.config)
						require.NoError(t, err)
						b = []byte(strings.Replace(string(b), `"smt":false`, `"smt":false,"unknown":true`, 1))
						require.NoError(t, os.WriteFile(f.config, b, 0600))
					}
				case "cold-watch":
					if f.host != nil {
						f.host.lostWatch = true
					} else {
						require.NoError(t, os.WriteFile(filepath.Join(f.dir, "api.socket"), nil, 0600))
					}
				case "config":
					f.q.Deployment.Allocation.VCPU++
				case "image":
					require.NoError(t, os.WriteFile(f.base, []byte("foreign base"), 0600))
				case "writable":
					require.NoError(t, os.WriteFile(f.disk, []byte("changed guest data"), 0600))
				case "inode":
					b, err := os.ReadFile(f.disk)
					require.NoError(t, err)
					require.NoError(t, os.Rename(f.disk, f.disk+".old"))
					require.NoError(t, os.WriteFile(f.disk, b, 0600))
				case "unbound":
					f.q.Operation.Adoption = nil
				case "caller-pin":
					f.q.Deployment.ConfigDigest = vm.DigestBytes([]byte("unverified"))
				case "lineage":
					if f.host != nil {
						f.host.foreignBase = true
					} else {
						require.NoError(t, os.WriteFile(f.disk, make([]byte, 4096), 0600))
						f.q.Operation.Adoption.Components[1].Digest = vm.DigestBytes(make([]byte, 4096))
						f.q.Operation.Adoption.Digest = domain.VMAdoptionDigest(*f.q.Operation.Adoption)
					}
				case "foreign-owner":
					marker := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: f.q.Deployment.Identity, AppliedGeneration: 1, OperationID: uuid.New(), ImageDigest: f.q.Image.ManifestDigest, ConfigDigest: f.q.Deployment.ConfigDigest}
					marker.OrgID = uuid.New()
					b, err := json.Marshal(marker)
					require.NoError(t, err)
					if f.host != nil {
						var escaped strings.Builder
						require.NoError(t, xml.EscapeText(&escaped, b))
						f.host.xml = []byte(strings.Replace(string(f.host.xml), "<metadata></metadata>", `<metadata><ownership xmlns="urn:bahia:persistent-vm:2">`+escaped.String()+`</ownership></metadata>`, 1))
					} else {
						require.NoError(t, os.WriteFile(filepath.Join(f.dir, "ownership.json"), b, 0600))
					}
				}
				result, err := f.p.Execute(context.Background(), f.q)
				require.Error(t, err)
				require.False(t, result.Confirmed)
				_, err = os.Stat(filepath.Join(f.dir, "metadata.json"))
				require.True(t, os.IsNotExist(err), "applied record written")
				if f.host != nil {
					require.Zero(t, f.host.mutations)
				} else if change != "foreign-owner" {
					_, err = os.Stat(filepath.Join(f.dir, "ownership.json"))
					require.True(t, os.IsNotExist(err), "ownership written")
				}
			})
		}
	}
}

func TestMeasuredAdoptionRejectsDesiredConfiguration(t *testing.T) {
	for _, provider := range []domain.VMProvider{domain.VMProviderLibvirt, domain.VMProviderFirecracker} {
		for _, field := range []string{"cpu", "memory", "disk", "network", "autostart"} {
			t.Run(string(provider)+"/"+field, func(t *testing.T) {
				f := adoptionSetup(t, provider)
				want := f.q.Deployment
				switch field {
				case "cpu":
					want.Allocation.VCPU++
				case "memory":
					want.Allocation.MemoryBytes += 1 << 20
				case "disk":
					want.Allocation.DiskBytes++
				case "network":
					want.Network.Mode = domain.VMNetworkNAT
					ref := uuid.New()
					want.Network.NetworkRef = &ref
				case "autostart":
					want.Autostart = true
				}
				_, err := f.p.MeasureAdoption(context.Background(), domain.VMChangeRequest{Host: f.q.Host, Desired: want, Image: f.q.Image})
				require.Error(t, err)
				_, err = os.Stat(filepath.Join(f.dir, "metadata.json"))
				require.True(t, os.IsNotExist(err))
				if f.host != nil {
					require.Zero(t, f.host.mutations)
				}
			})
		}
	}
}

func TestAdoptionDriverRequiresProofBeforeOwnership(t *testing.T) {
	for _, provider := range []domain.VMProvider{domain.VMProviderLibvirt, domain.VMProviderFirecracker} {
		t.Run(string(provider), func(t *testing.T) {
			f := adoptionSetup(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			r, err := f.driver.InspectPersistent(ctx, f.q.Deployment.Identity.ProviderResourceID)
			require.NoError(t, err)
			m := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: f.q.Deployment.Identity, AppliedGeneration: 1, OperationID: f.q.Operation.ID, ImageDigest: f.q.Image.ManifestDigest, ConfigDigest: f.q.Deployment.ConfigDigest}
			require.Error(t, f.driver.AdoptPersistent(ctx, r, m))
			r, err = f.driver.InspectPersistent(ctx, r.ID)
			require.NoError(t, err)
			require.Nil(t, r.Marker)
		})
	}
}

func TestMeasuredAdoptionRejectsUnknownControllerMetadata(t *testing.T) {
	f := adoptionSetup(t, domain.VMProviderLibvirt)
	f.host.xml = []byte(strings.Replace(string(f.host.xml), "<metadata></metadata>", `<metadata><job xmlns="urn:another-controller:jobs">opaque-job</job></metadata>`, 1))
	_, err := f.p.MeasureAdoption(context.Background(), domain.VMChangeRequest{Host: f.q.Host, Desired: f.q.Deployment, Image: f.q.Image})
	require.Error(t, err)
	require.Zero(t, f.host.mutations)
	_, err = os.Stat(filepath.Join(f.dir, "metadata.json"))
	require.True(t, os.IsNotExist(err))
}

func TestMeasuredAdoptionRejectsForeignOrUnknownRecord(t *testing.T) {
	for _, provider := range []domain.VMProvider{domain.VMProviderLibvirt, domain.VMProviderFirecracker} {
		for _, version := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("%s/schema-%d", provider, version), func(t *testing.T) {
				f := adoptionSetup(t, provider)
				marker := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: f.q.Deployment.Identity, AppliedGeneration: 1, OperationID: uuid.New(), ImageDigest: f.q.Image.ManifestDigest, ConfigDigest: f.q.Deployment.ConfigDigest}
				marker.OrgID = uuid.New()
				before, err := json.Marshal(map[string]any{"schema_version": version, "marker": marker, "ownership_id": uuid.New()})
				require.NoError(t, err)
				path := filepath.Join(f.dir, "metadata.json")
				require.NoError(t, os.WriteFile(path, before, 0600))
				_, err = f.p.MeasureAdoption(context.Background(), domain.VMChangeRequest{Host: f.q.Host, Desired: f.q.Deployment, Image: f.q.Image})
				require.Error(t, err)
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, before, after)
				if f.host != nil {
					require.Zero(t, f.host.mutations)
				} else {
					_, err = os.Stat(filepath.Join(f.dir, "ownership.json"))
					require.True(t, os.IsNotExist(err))
				}
			})
		}
	}
}

func TestMeasuredStandaloneQCOWSnapshot(t *testing.T) {
	f := adoptionSetup(t, domain.VMProviderLibvirt)
	f.host.standalone = true
	f.measure(t)
	result, err := f.p.Execute(context.Background(), f.q)
	require.NoError(t, err)
	require.True(t, result.Confirmed)
}

func TestMeasuredAdoptionCoordinatedUEFIAndTPM(t *testing.T) {
	for _, changed := range []string{"matching", "nvram", "swtpm", "firmware"} {
		t.Run(changed, func(t *testing.T) {
			f := adoptionSetup(t, domain.VMProviderLibvirt)
			release := filepath.Dir(f.base)
			template := []byte("trusted variable template")
			require.NoError(t, os.WriteFile(filepath.Join(release, "uefi-vars.fd"), template, 0600))
			data, err := os.ReadFile(filepath.Join(release, "manifest.json"))
			require.NoError(t, err)
			var manifest vm.Manifest
			require.NoError(t, json.Unmarshal(data, &manifest))
			manifest.SHA256["uefi_vars"] = strings.TrimPrefix(vm.DigestBytes(template), "sha256:")
			data, err = json.Marshal(manifest)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(release, "manifest.json"), data, 0600))
			f.q.Image.ManifestDigest = vm.DigestBytes(data)
			f.q.Image.Firmware, f.q.Deployment.Firmware = domain.VMFirmwareUEFI, domain.VMFirmwareUEFI
			f.q.Image.Components = append(f.q.Image.Components, domain.VMComponent{Kind: domain.VMComponentNVRAM, StorageRef: uuid.New(), Digest: vm.DigestBytes(template), SizeBytes: int64(len(template))})
			tpmID := uuid.New()
			f.q.Deployment.TPM = domain.VMTPM{Enabled: true, IdentityID: &tpmID}
			nvram, tpm := filepath.Join(f.dir, "nvram.fd"), filepath.Join(f.dir, "swtpm")
			require.NoError(t, os.WriteFile(nvram, []byte("measured variables"), 0600))
			require.NoError(t, os.Mkdir(tpm, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(tpm, "tpm2-00.permall"), []byte("measured TPM identity"), 0600))
			f.host.xml = []byte(strings.Replace(string(f.host.xml), "</os>", `<loader readonly="yes" type="pflash">`+f.firmware+`</loader><nvram>`+nvram+`</nvram></os>`, 1))
			f.host.xml = []byte(strings.Replace(string(f.host.xml), "<devices>", `<devices><tpm model="tpm-crb"><backend type="emulator" version="2.0"><source type="dir" path="`+tpm+`"/></backend></tpm>`, 1))
			f.measure(t)
			require.Len(t, f.q.Operation.Adoption.Components, 3)
			require.Len(t, f.q.Operation.Adoption.ProviderEvidence, 1)
			switch changed {
			case "nvram":
				require.NoError(t, os.WriteFile(nvram, []byte("changed variables"), 0600))
			case "swtpm":
				require.NoError(t, os.WriteFile(filepath.Join(tpm, "tpm2-00.permall"), []byte("changed identity"), 0600))
			case "firmware":
				require.NoError(t, os.WriteFile(f.firmware, []byte("different firmware"), 0600))
			}
			result, err := f.p.Execute(context.Background(), f.q)
			if changed == "matching" {
				require.NoError(t, err)
				require.True(t, result.Confirmed)
			} else {
				require.Error(t, err)
				require.Zero(t, f.host.mutations)
				_, err = os.Stat(filepath.Join(f.dir, "metadata.json"))
				require.True(t, os.IsNotExist(err))
			}
		})
	}
}
