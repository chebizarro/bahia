package libvirt

import (
	"bytes"
	"encoding/xml"
	"fmt"
)

// domainParams collects everything the persistent-domain XML needs.
type domainParams struct {
	Name         string
	MemoryMB     int
	VCPUs        int
	Arch         string // "" -> host architecture
	Overlay      string // per-instance qcow2 overlay
	NVRAM        string // per-instance UEFI vars copy ("" -> BIOS boot)
	FirmwareCode string // read-only UEFI firmware code (used when NVRAM set)
	ConsoleLog   string // serial console log file
	VsockCID     uint32 // 0 -> no vsock device
}

// domainXML renders a persistent KVM domain definition. Unlike the
// one-shot job engines (transient virsh create, on_reboot=destroy), these
// domains survive reboots and bahia restarts: virsh define keeps the
// definition, on_reboot=restart keeps long-lived guests running.
//
// V1 domains are vsock+console only — no network interface is attached
// (networking is deferred per the VM runtime plan).
func domainXML(p domainParams) ([]byte, error) {
	if p.Name == "" {
		return nil, fmt.Errorf("domain name is required")
	}
	if p.MemoryMB <= 0 || p.VCPUs <= 0 {
		return nil, fmt.Errorf("domain %q requires positive memory (%d MiB) and vcpus (%d)", p.Name, p.MemoryMB, p.VCPUs)
	}
	if p.Overlay == "" {
		return nil, fmt.Errorf("domain %q requires an overlay disk path", p.Name)
	}
	if p.ConsoleLog == "" {
		return nil, fmt.Errorf("domain %q requires a console log path", p.Name)
	}
	if p.NVRAM != "" && p.FirmwareCode == "" {
		return nil, fmt.Errorf("domain %q has UEFI vars but no firmware code image configured", p.Name)
	}

	arch := p.Arch
	if arch == "" {
		arch = hostGuestArch()
	}
	machine := "q35"
	if arch == "aarch64" {
		machine = "virt"
	}

	var doc bytes.Buffer
	doc.WriteString(xml.Header)
	fmt.Fprintf(&doc, "<domain type=\"kvm\">\n  <name>%s</name>\n", xmlText(p.Name))
	fmt.Fprintf(&doc, "  <memory unit=\"MiB\">%d</memory>\n", p.MemoryMB)
	fmt.Fprintf(&doc, "  <vcpu placement=\"static\">%d</vcpu>\n", p.VCPUs)
	doc.WriteString("  <os>\n")
	fmt.Fprintf(&doc, "    <type arch=\"%s\" machine=\"%s\">hvm</type>\n", xmlText(arch), xmlText(machine))
	if p.NVRAM != "" {
		fmt.Fprintf(&doc, "    <loader readonly=\"yes\" type=\"pflash\">%s</loader>\n", xmlText(p.FirmwareCode))
		fmt.Fprintf(&doc, "    <nvram>%s</nvram>\n", xmlText(p.NVRAM))
	}
	doc.WriteString("  </os>\n")
	doc.WriteString("  <features><acpi/><apic/></features>\n")
	doc.WriteString("  <cpu mode=\"host-passthrough\" check=\"none\"/>\n")
	doc.WriteString("  <clock offset=\"utc\"/>\n")
	doc.WriteString("  <on_poweroff>destroy</on_poweroff>\n")
	doc.WriteString("  <on_reboot>restart</on_reboot>\n")
	doc.WriteString("  <on_crash>destroy</on_crash>\n")
	doc.WriteString("  <devices>\n")
	doc.WriteString("    <disk type=\"file\" device=\"disk\">\n")
	doc.WriteString("      <driver name=\"qemu\" type=\"qcow2\" cache=\"none\" discard=\"unmap\"/>\n")
	fmt.Fprintf(&doc, "      <source file=\"%s\"/>\n", xmlText(p.Overlay))
	doc.WriteString("      <target dev=\"vda\" bus=\"virtio\"/>\n")
	doc.WriteString("    </disk>\n")
	fmt.Fprintf(&doc, "    <serial type=\"file\">\n      <source path=\"%s\" append=\"on\"/>\n      <target port=\"0\"/>\n    </serial>\n", xmlText(p.ConsoleLog))
	fmt.Fprintf(&doc, "    <console type=\"file\">\n      <source path=\"%s\" append=\"on\"/>\n      <target type=\"serial\" port=\"0\"/>\n    </console>\n", xmlText(p.ConsoleLog))
	if p.VsockCID != 0 {
		fmt.Fprintf(&doc, "    <vsock model=\"virtio\"><cid auto=\"no\" address=\"%d\"/></vsock>\n", p.VsockCID)
	}
	doc.WriteString("  </devices>\n</domain>\n")
	return doc.Bytes(), nil
}

func xmlText(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}
