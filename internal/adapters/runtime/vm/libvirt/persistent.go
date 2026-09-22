package libvirt

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

const ownershipNamespace = "urn:bahia:persistent-vm:2"
const legacyNamespace = "urn:bahia:legacy-vm:1"

type persistentXML struct {
	UUID     string `xml:"uuid"`
	Name     string `xml:"name"`
	Metadata struct {
		Owners []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"metadata"`
	OS struct {
		NVRAM string `xml:"nvram"`
	} `xml:"os"`
	Devices struct {
		Disks []struct {
			Device   string    `xml:"device,attr"`
			Type     string    `xml:"type,attr"`
			ReadOnly *struct{} `xml:"readonly"`
			Source   struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
		} `xml:"disk"`
		TPMs []struct {
			Backend struct {
				Type   string `xml:"type,attr"`
				Source struct {
					Type string `xml:"type,attr"`
					Path string `xml:"path,attr"`
				} `xml:"source"`
			} `xml:"backend"`
		} `xml:"tpm"`
	} `xml:"devices"`
}

func (d *Driver) persistentBackend() error {
	if d.cfg.URI != "qemu:///system" && d.cfg.URI != "qemu:///session" {
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	if !filepath.IsAbs(d.cfg.InstancesDir) {
		return vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	return nil
}
func (d *Driver) ListPersistent(ctx context.Context) ([]uuid.UUID, error) {
	if err := d.persistentBackend(); err != nil {
		return nil, err
	}
	out, err := d.virsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, value := range strings.Fields(string(out)) {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || seen[id] {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, err)
		}
		ids = append(ids, id)
		seen[id] = true
	}
	return ids, nil
}
func (d *Driver) InspectPersistent(ctx context.Context, id uuid.UUID) (*vm.PersistentResource, error) {
	if id == uuid.Nil {
		return nil, vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	ids, err := d.ListPersistent(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, v := range ids {
		if v == id {
			found = true
		}
	}
	r := &vm.PersistentResource{ID: id, State: domain.VMRuntimeAbsent, Components: map[domain.VMComponentKind]string{}}
	if !found {
		return r, nil
	}
	data, err := d.virsh(ctx, "dumpxml", id.String(), "--inactive")
	if err != nil {
		return nil, err
	}
	var doc persistentXML
	if err = xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	parsed, err := uuid.Parse(doc.UUID)
	if err != nil || parsed != id {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, err)
	}
	for _, m := range doc.Metadata.Owners {
		if m.XMLName.Space == ownershipNamespace && m.XMLName.Local == "ownership" {
			if r.Marker != nil {
				return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
			}
			var marker domain.VMOwnershipMarker
			if err = json.Unmarshal([]byte(m.Value), &marker); err != nil {
				return nil, err
			}
			r.Marker = &marker
		}
	}
	for _, disk := range doc.Devices.Disks {
		if disk.ReadOnly != nil {
			continue
		}
		if disk.Device != "disk" || disk.Type != "file" || disk.Source.File == "" || r.Components[domain.VMComponentDisk] != "" {
			return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		r.Components[domain.VMComponentDisk] = disk.Source.File
	}
	if doc.OS.NVRAM != "" {
		r.Components[domain.VMComponentNVRAM] = strings.TrimSpace(doc.OS.NVRAM)
	}
	if len(doc.Devices.TPMs) > 1 {
		return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	for _, tpm := range doc.Devices.TPMs {
		if tpm.Backend.Type != "emulator" || tpm.Backend.Source.Type != "dir" || tpm.Backend.Source.Path == "" {
			return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		r.Components[domain.VMComponentSWTPM] = tpm.Backend.Source.Path
	}
	fingerprint, err := normalizedXMLDigest(data)
	if err != nil {
		return nil, err
	}
	r.Fingerprint = fingerprint
	state, err := d.State(ctx, id.String())
	if err != nil {
		return nil, err
	}
	switch state {
	case vm.StateRunning:
		r.State = domain.VMRuntimeRunning
	case vm.StateStopped:
		r.State = domain.VMRuntimeStopped
	case vm.StatePaused:
		r.State = domain.VMRuntimePaused
	case vm.StateCrashed:
		r.State = domain.VMRuntimeFailed
	default:
		return nil, vm.ProviderError(domain.VMErrorUnavailable, nil)
	}
	return r, nil
}

// Normalize insignificant XML whitespace and attribute ordering while retaining
// all provider configuration, including elements this driver does not actuate.
func normalizedXMLDigest(data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	skip := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if t.Name.Local == "ownership" && t.Name.Space == ownershipNamespace {
				skip = 1
				continue
			}
			// Namespace declarations are emitted by encoding/xml from expanded names.
			attrs := t.Attr[:0]
			for _, a := range t.Attr {
				if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
					attrs = append(attrs, a)
				}
			}
			t.Attr = attrs
			sort.Slice(t.Attr, func(i, j int) bool {
				return t.Attr[i].Name.Space+t.Attr[i].Name.Local < t.Attr[j].Name.Space+t.Attr[j].Name.Local
			})
			token = t
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
		case xml.CharData:
			if skip > 0 || strings.TrimSpace(string(t)) == "" {
				continue
			}
			token = xml.CharData(strings.TrimSpace(string(t)))
		case xml.Comment, xml.ProcInst:
			continue
		}
		if err = enc.EncodeToken(token); err != nil {
			return "", err
		}
	}
	if err := enc.Flush(); err != nil {
		return "", err
	}
	return vm.DigestBytes(out.Bytes()), nil
}
func (d *Driver) recheck(ctx context.Context, r *vm.PersistentResource) error {
	actual, err := d.InspectPersistent(ctx, r.ID)
	if err != nil {
		return err
	}
	return vm.CheckPersistentResource(r, actual)
}

func (d *Driver) AdoptPersistent(ctx context.Context, r *vm.PersistentResource, marker domain.VMOwnershipMarker) error {
	if err := vm.CheckAdoptionMarker(r, marker); err != nil {
		return err
	}
	if err := d.recheck(ctx, r); err != nil {
		return err
	}
	if r.State == domain.VMRuntimeAbsent {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if r.Marker == nil && r.State != domain.VMRuntimeStopped {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if err := vm.CheckAdoptionFiles(ctx, r); err != nil {
		return err
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	value := "<ownership xmlns=\"" + ownershipNamespace + "\">" + xmlText(string(data)) + "</ownership>"
	args := []string{"metadata", r.ID.String(), "--uri", ownershipNamespace, "--key", "bahia", "--set", value, "--config"}
	if r.State == domain.VMRuntimeRunning || r.State == domain.VMRuntimePaused {
		args = append(args, "--live")
	}
	_, err = d.virsh(ctx, args...)
	return err
}

func (d *Driver) DefinePersistent(ctx context.Context, s vm.PersistentSpec, current *vm.PersistentResource) error {
	if err := d.persistentBackend(); err != nil {
		return err
	}
	if domain.ValidateVMOwnershipMarker(s.Marker) != nil || s.Instance.Name != s.Marker.ProviderResourceID.String() || s.Instance.InstanceDir != d.instanceDir(s.Instance.Name) {
		return vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	if err := vm.CheckDefinitionBaseline(current, s.Marker); err != nil {
		return err
	}
	if err := vm.CheckWritableComponents(s.Instance.InstanceDir, s.Components); err != nil {
		return err
	}
	if current.State != domain.VMRuntimeAbsent && current.State != domain.VMRuntimeStopped {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if err := d.recheck(ctx, current); err != nil {
		return err
	}
	var networkType, networkName string
	if s.Deployment.Network.Mode != "" && s.Deployment.Network.Mode != domain.VMNetworkIsolated {
		if s.Deployment.Network.NetworkRef == nil {
			return vm.ProviderError(domain.VMErrorInvalid, nil)
		}
		binding, ok := d.cfg.Networks[*s.Deployment.Network.NetworkRef]
		if !ok || binding.Mode != s.Deployment.Network.Mode || !validNetworkName(binding.Name) {
			return vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		switch binding.Mode {
		case domain.VMNetworkNAT:
			networkType = "network"
		case domain.VMNetworkBridged:
			networkType = "bridge"
		default:
			return vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		networkName = binding.Name
	}
	if len(s.Deployment.Network.PassthroughDeviceRefs) > 0 {
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	components := s.Components
	if components == nil {
		components = map[domain.VMComponentKind]string{}
	}
	if len(components) == 0 {
		if s.Instance.Image.Format != vm.FormatQCOW2 {
			return vm.ProviderError(domain.VMErrorInvalid, nil)
		}
		overlay := filepath.Join(s.Instance.InstanceDir, overlayFileName)
		if _, err := d.qemuImg(ctx, "create", "-f", "qcow2", "-F", "qcow2", "-b", s.Instance.Image.DiskPath, overlay); err != nil {
			return err
		}
		components[domain.VMComponentDisk] = overlay
		if s.Deployment.Firmware == domain.VMFirmwareUEFI {
			if s.Instance.Image.UEFIVarsPath == "" {
				return vm.ProviderError(domain.VMErrorIntegrity, nil)
			}
			nvram := filepath.Join(s.Instance.InstanceDir, nvramFileName)
			if err := vm.CopyRegularFile(ctx, s.Instance.Image.UEFIVarsPath, nvram); err != nil {
				return err
			}
			components[domain.VMComponentNVRAM] = nvram
		}
		if s.Deployment.TPM.Enabled {
			path := filepath.Join(s.Instance.InstanceDir, "swtpm")
			if err := os.Mkdir(path, 0700); err != nil {
				return err
			}
			components[domain.VMComponentSWTPM] = path
		}
	}
	for _, kind := range vm.RequiredComponents(domain.VMProviderLibvirt, s.Deployment.Firmware, s.Deployment.TPM.Enabled) {
		if components[kind] == "" {
			return vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
		if err := vm.CheckContainedPath(s.Instance.InstanceDir, components[kind]); err != nil {
			return err
		}
	}
	// Refuse shrink even when a caller supplies an inaccurate current allocation.
	info, err := d.qemuImg(ctx, "info", "--output=json", components[domain.VMComponentDisk])
	if err != nil {
		return err
	}
	var disk struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err = json.Unmarshal(info, &disk); err != nil {
		return err
	}
	if disk.VirtualSize <= 0 || s.Deployment.Allocation.DiskBytes < disk.VirtualSize {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if s.Deployment.Allocation.DiskBytes > disk.VirtualSize {
		if _, err = d.qemuImg(ctx, "resize", components[domain.VMComponentDisk], strconv.FormatInt(s.Deployment.Allocation.DiskBytes, 10)); err != nil {
			return err
		}
	}
	arch := s.Instance.Image.Arch
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	params := domainParams{NetworkType: networkType, NetworkName: networkName, Name: s.Instance.Name, MemoryMB: s.Instance.MemoryMB, VCPUs: s.Instance.VCPUs, Arch: arch, Overlay: components[domain.VMComponentDisk], NVRAM: components[domain.VMComponentNVRAM], FirmwareCode: d.cfg.FirmwareCodePath, ConsoleLog: filepath.Join(s.Instance.InstanceDir, consoleLogFileName), VsockCID: s.Instance.VsockCID, Marker: &s.Marker, TPMState: components[domain.VMComponentSWTPM]}
	data, err := domainXML(params)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Instance.InstanceDir, "definition-"+s.Marker.OperationID.String()+".xml")
	if err = os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	if err = d.recheck(ctx, current); err != nil {
		return err
	}
	if _, err = d.virsh(ctx, "define", path); err != nil {
		return err
	}
	args := []string{"autostart", s.Instance.Name}
	if !s.Deployment.Autostart {
		args = append(args, "--disable")
	}
	_, err = d.virsh(ctx, args...)
	return err
}

func (d *Driver) TransitionPersistent(ctx context.Context, r *vm.PersistentResource, kind domain.VMOperationKind, force bool) error {
	if r.Marker == nil || domain.ValidateVMOwnershipMarker(*r.Marker) != nil || r.Marker.ProviderResourceID != r.ID {
		return vm.ProviderError(domain.VMErrorForeign, nil)
	}
	if err := d.recheck(ctx, r); err != nil {
		return err
	}
	switch kind {
	case domain.VMOperationStart:
		if r.State == domain.VMRuntimeRunning {
			return nil
		}
		if r.State != domain.VMRuntimeStopped {
			return vm.ProviderError(domain.VMErrorConflict, nil)
		}
		return d.eventTransition(ctx, r, "start", domain.VMRuntimeRunning)
	case domain.VMOperationGracefulStop:
		if r.State == domain.VMRuntimeStopped {
			return nil
		}
		if r.State != domain.VMRuntimeRunning {
			return vm.ProviderError(domain.VMErrorConflict, nil)
		}
		return d.eventTransition(ctx, r, "shutdown", domain.VMRuntimeStopped)
	case domain.VMOperationReboot:
		if r.State != domain.VMRuntimeRunning {
			return vm.ProviderError(domain.VMErrorConflict, nil)
		}
		return d.eventTransition(ctx, r, "reboot", domain.VMRuntimeRunning)
	case domain.VMOperationDelete:
		if r.State != domain.VMRuntimeStopped {
			if !force {
				return vm.ProviderError(domain.VMErrorApprovalRequired, nil)
			}
			if err := d.eventTransition(ctx, r, "destroy", domain.VMRuntimeStopped); err != nil {
				return err
			}
			fresh, err := d.InspectPersistent(ctx, r.ID)
			if err != nil {
				return err
			}
			r = fresh
		}
		if err := d.recheck(ctx, r); err != nil {
			return err
		}
		// Preserve NVRAM/TPM until the core confirms absence, then clean only owned
		// private storage. Never let virsh remove external adopted state implicitly.
		_, err := d.virsh(ctx, "undefine", r.ID.String(), "--keep-nvram", "--keep-tpm")
		return err
	}
	return vm.ProviderError(domain.VMErrorUnsupported, nil)
}

// A name-scanned v1 runtime may still observe an explicitly adopted domain,
// but cannot mutate it after a v2 ownership marker has been installed.
func (d *Driver) rejectPersistentLegacyMutation(ctx context.Context, name string) error {
	return d.VerifyLegacy(ctx, name, uuid.Nil)
}

func (d *Driver) VerifyLegacy(ctx context.Context, name string, expected uuid.UUID) error {
	proof, err := vm.ReadLegacyProof(d.cfg.InstancesDir, name)
	if err != nil {
		return err
	}
	if expected != uuid.Nil && proof.ID != expected {
		return vm.ProviderError(domain.VMErrorForeign, nil)
	}
	data, err := d.virsh(ctx, "dumpxml", name, "--inactive")
	if err != nil {
		return err
	}
	var doc persistentXML
	if err = xml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if doc.UUID != proof.ID.String() || doc.Name != name {
		return vm.ProviderError(domain.VMErrorForeign, nil)
	}
	found := 0
	for _, entry := range doc.Metadata.Owners {
		if entry.XMLName.Space == ownershipNamespace {
			return vm.ProviderError(domain.VMErrorForeign, nil)
		}
		if entry.XMLName.Space == legacyNamespace && entry.XMLName.Local == "ownership" && entry.Value == proof.ID.String() {
			found++
		}
	}
	digest, err := normalizedXMLDigest(data)
	if err != nil || found != 1 || digest != proof.DefinitionDigest {
		return vm.ProviderError(domain.VMErrorForeign, err)
	}
	return nil
}

func validNetworkName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] == '-' {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

var _ vm.PersistentDriver = (*Driver)(nil)
