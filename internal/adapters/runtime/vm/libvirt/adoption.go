package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"path/filepath"
	"strings"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

type adoptionXML struct {
	Metadata struct {
		Entries []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"metadata"`
	Type   string `xml:"type,attr"`
	Memory struct {
		Unit  string `xml:"unit,attr"`
		Value int64  `xml:",chardata"`
	} `xml:"memory"`
	CurrentMemory *struct {
		Unit  string `xml:"unit,attr"`
		Value int64  `xml:",chardata"`
	} `xml:"currentMemory"`
	VCPU struct {
		Current int64 `xml:"current,attr"`
		Value   int64 `xml:",chardata"`
	} `xml:"vcpu"`
	OS struct {
		Type struct {
			Arch    string `xml:"arch,attr"`
			Machine string `xml:"machine,attr"`
			Value   string `xml:",chardata"`
		} `xml:"type"`
		Loader struct {
			Readonly string `xml:"readonly,attr"`
			Type     string `xml:"type,attr"`
			Path     string `xml:",chardata"`
		} `xml:"loader"`
		NVRAM   string `xml:"nvram"`
		Kernel  string `xml:"kernel"`
		Initrd  string `xml:"initrd"`
		Cmdline string `xml:"cmdline"`
		Boots   []struct {
			Dev string `xml:"dev,attr"`
		} `xml:"boot"`
	} `xml:"os"`
	Devices struct {
		Disks []struct {
			Type         string    `xml:"type,attr"`
			Device       string    `xml:"device,attr"`
			Readonly     *struct{} `xml:"readonly"`
			BackingStore *struct {
				Source *struct{} `xml:"source"`
			} `xml:"backingStore"`
			DataStore *struct{} `xml:"dataStore"`
			Driver    struct {
				Type string `xml:"type,attr"`
			} `xml:"driver"`
			Source struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
			Target struct {
				Dev string `xml:"dev,attr"`
				Bus string `xml:"bus,attr"`
			} `xml:"target"`
		} `xml:"disk"`
		Interfaces []struct {
			Type   string `xml:"type,attr"`
			Source struct {
				Network string `xml:"network,attr"`
				Bridge  string `xml:"bridge,attr"`
			} `xml:"source"`
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
		} `xml:"interface"`
		Hostdev    []struct{} `xml:"hostdev"`
		Filesystem []struct{} `xml:"filesystem"`
		TPM        []struct {
			Model   string `xml:"model,attr"`
			Backend struct {
				Version string `xml:"version,attr"`
			} `xml:"backend"`
		} `xml:"tpm"`
	} `xml:"devices"`
	OnReboot      string    `xml:"on_reboot"`
	Commandline   *struct{} `xml:"commandline"`
	MemoryBacking *struct{} `xml:"memoryBacking"`
}

func memoryBytes(unit string, value int64) int64 {
	switch unit {
	case "b", "bytes":
		return value
	case "", "KiB", "k":
		if value > 0 && value <= 1<<50 {
			return value << 10
		}
	case "MiB", "M":
		if value > 0 && value <= 1<<40 {
			return value << 20
		}
	case "GiB", "G":
		if value > 0 && value <= 1<<30 {
			return value << 30
		}
	}
	return -1
}

func (d *Driver) MeasurePersistent(ctx context.Context, r *vm.PersistentResource, want domain.PersistentVMDeployment, release *vm.Release) (*vm.AdoptionProof, error) {
	if err := d.recheck(ctx, r); err != nil {
		return nil, err
	}
	data, err := d.virsh(ctx, "dumpxml", r.ID.String(), "--inactive")
	if err != nil {
		return nil, err
	}
	fingerprint, err := normalizedXMLDigest(data)
	if err != nil || fingerprint != r.Fingerprint {
		return nil, vm.ProviderError(domain.VMErrorConflict, err)
	}
	var doc adoptionXML
	if err = xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	for _, entry := range doc.Metadata.Entries {
		// Unknown metadata may assert another controller's ownership (including
		// Loom). Neither a name prefix nor matching image bytes overrides it.
		if entry.XMLName.Local != "ownership" || (entry.XMLName.Space != ownershipNamespace && entry.XMLName.Space != legacyNamespace) {
			return nil, vm.ProviderError(domain.VMErrorForeign, nil)
		}
		if entry.XMLName.Space == legacyNamespace && strings.TrimSpace(entry.Value) != r.ID.String() {
			return nil, vm.ProviderError(domain.VMErrorForeign, nil)
		}
	}
	arch := release.Manifest.Arch
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	if doc.Type != "kvm" || doc.OS.Type.Value != "hvm" || doc.OS.Type.Arch != arch || doc.VCPU.Value != want.Allocation.VCPU || (doc.VCPU.Current != 0 && doc.VCPU.Current != doc.VCPU.Value) || memoryBytes(doc.Memory.Unit, doc.Memory.Value) != want.Allocation.MemoryBytes || (doc.CurrentMemory != nil && memoryBytes(doc.CurrentMemory.Unit, doc.CurrentMemory.Value) != want.Allocation.MemoryBytes) || doc.OS.Kernel != "" || doc.OS.Initrd != "" || doc.OS.Cmdline != "" || doc.OnReboot != "restart" || doc.Commandline != nil || doc.MemoryBacking != nil || len(doc.Devices.Hostdev) != 0 || len(doc.Devices.Filesystem) != 0 || len(want.Network.PassthroughDeviceRefs) != 0 || len(doc.Devices.Disks) != 1 {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	for _, boot := range doc.OS.Boots {
		if boot.Dev != "hd" {
			return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
	}
	disk := doc.Devices.Disks[0]
	if disk.Type != "file" || disk.Device != "disk" || disk.Readonly != nil || (disk.BackingStore != nil && disk.BackingStore.Source != nil) || disk.DataStore != nil || disk.Driver.Type != "qcow2" || disk.Source.File != r.Components[domain.VMComponentDisk] || disk.Target.Dev != "vda" || disk.Target.Bus != "virtio" {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	if want.Firmware == domain.VMFirmwareUEFI {
		if strings.TrimSpace(doc.OS.NVRAM) != r.Components[domain.VMComponentNVRAM] || doc.OS.Loader.Readonly != "yes" || doc.OS.Loader.Type != "pflash" || strings.TrimSpace(doc.OS.Loader.Path) != d.cfg.FirmwareCodePath || release.UEFIVarsPath == "" {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
	} else if want.Firmware != domain.VMFirmwareBIOS || doc.OS.NVRAM != "" || doc.OS.Loader.Path != "" {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	if want.TPM.Enabled != (len(doc.Devices.TPM) == 1) || len(doc.Devices.TPM) > 1 {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	if want.TPM.Enabled && (doc.Devices.TPM[0].Model != "tpm-crb" || doc.Devices.TPM[0].Backend.Version != "2.0") {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	if want.Network.Mode == domain.VMNetworkIsolated {
		if len(doc.Devices.Interfaces) != 0 || want.Network.NetworkRef != nil {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
	} else {
		if want.Network.NetworkRef == nil || len(doc.Devices.Interfaces) != 1 {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
		binding, ok := d.cfg.Networks[*want.Network.NetworkRef]
		i := doc.Devices.Interfaces[0]
		if !ok || binding.Mode != want.Network.Mode || i.Model.Type != "virtio" || (binding.Mode == domain.VMNetworkNAT && (i.Type != "network" || i.Source.Network != binding.Name || i.Source.Bridge != "")) || (binding.Mode == domain.VMNetworkBridged && (i.Type != "bridge" || i.Source.Bridge != binding.Name || i.Source.Network != "")) {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
	}
	info, err := d.virsh(ctx, "dominfo", r.ID.String())
	if err != nil {
		return nil, err
	}
	autostart, persistent := "", ""
	for _, line := range strings.Split(string(info), "\n") {
		key, value, _ := strings.Cut(line, ":")
		switch strings.TrimSpace(key) {
		case "Autostart":
			autostart = strings.TrimSpace(value)
		case "Persistent":
			persistent = strings.TrimSpace(value)
		}
	}
	if persistent != "yes" || (autostart != "enable" && autostart != "disable") || want.Autostart != (autostart == "enable") {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	// Accept a standalone snapshot or one private overlay over the verified
	// immutable catalog base. Extra/remote/external-data stores lack proof.
	data, err = d.qemuImg(ctx, "info", "--backing-chain", "--output=json", disk.Source.File)
	if err != nil {
		return nil, err
	}
	// External qcow2 data files can appear inside format-specific data. Refuse
	// every occurrence, not just a top-level field that older qemu versions use.
	var raw any
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if externalImageData(raw) {
		return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	var chain []struct {
		Filename      string `json:"filename"`
		Format        string `json:"format"`
		Backing       string `json:"full-backing-filename"`
		BackingName   string `json:"backing-filename"`
		BackingFormat string `json:"backing-filename-format"`
		DataFile      string `json:"data-file"`
		VirtualSize   int64  `json:"virtual-size"`
		Encrypted     bool   `json:"encrypted"`
	}
	if err = json.Unmarshal(data, &chain); err != nil {
		return nil, err
	}
	if len(chain) < 1 || len(chain) > 2 || chain[0].Filename != disk.Source.File || chain[0].VirtualSize != want.Allocation.DiskBytes {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	for _, c := range chain {
		if c.Format != "qcow2" || c.DataFile != "" || c.Encrypted || c.VirtualSize <= 0 || !filepath.IsAbs(c.Filename) {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
	}
	if len(chain) == 2 {
		if chain[0].Backing != release.DiskPath || chain[0].BackingFormat != "qcow2" || chain[1].Filename != release.DiskPath || chain[1].Backing != "" || chain[1].BackingName != "" || chain[0].VirtualSize != chain[1].VirtualSize {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
	} else {
		if chain[0].Backing != "" || chain[0].BackingName != "" {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
		// Standalone writable disks may enroll against a separately published
		// snapshot. Its own graph must also be standalone, not an unhashed parent.
		data, err = d.qemuImg(ctx, "info", "--backing-chain", "--output=json", release.DiskPath)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		if externalImageData(raw) {
			return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		if err = json.Unmarshal(data, &chain); err != nil {
			return nil, err
		}
		if len(chain) != 1 || chain[0].Filename != release.DiskPath || chain[0].Format != "qcow2" || chain[0].Backing != "" || chain[0].BackingName != "" || chain[0].Encrypted || chain[0].VirtualSize != want.Allocation.DiskBytes {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
	}
	base, err := vm.MeasureAdoptionFile(ctx, release.DiskPath)
	if err != nil {
		return nil, err
	}
	if base.Digest != "sha256:"+release.Manifest.SHA256["disk"] {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	// A backing filename alone can be forged with an unsafe rebase. Compare the
	// entire guest-visible disk to the trusted catalog snapshot before assigning
	// its image pin; the overlay's own measured bytes remain separately approved.
	if _, err = d.qemuImg(ctx, "compare", "-f", "qcow2", "-F", "qcow2", disk.Source.File, release.DiskPath); err != nil {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, err)
	}
	sources := map[domain.VMComponentKind]string{domain.VMComponentDisk: release.DiskPath}
	if want.Firmware == domain.VMFirmwareUEFI {
		sources[domain.VMComponentNVRAM] = release.UEFIVarsPath
	}
	if want.TPM.Enabled {
		sources[domain.VMComponentSWTPM] = r.Components[domain.VMComponentSWTPM]
	}
	proof := &vm.AdoptionProof{Sources: sources}
	if want.Firmware == domain.VMFirmwareUEFI {
		file, err := vm.MeasureAdoptionFile(ctx, d.cfg.FirmwareCodePath)
		if err != nil {
			return nil, err
		}
		proof.Files = append(proof.Files, file)
	}
	return proof, nil
}

func externalImageData(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			if (key == "data-file" || key == "data-file-raw") && value != "" && value != false && value != nil {
				return true
			}
			if externalImageData(value) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if externalImageData(value) {
				return true
			}
		}
	}
	return false
}
