// Package board loads and represents board profiles. A board profile is a
// JSON file that defines everything board-specific: kernel source, firmware
// URLs, required modules, network interface, and boot configuration.
package board

import (
	"encoding/json"
	"fmt"
	"os"
)

// Board defines a target board's hardware profile.
type Board struct {
	Name             string   `json:"name"`
	Arch             string   `json:"arch"`
	PageSize         uint64   `json:"page_size"`
	NetworkInterface string   `json:"network_interface"`
	Kernel           Kernel   `json:"kernel"`
	Firmware         []Asset  `json:"firmware,omitempty"`
	DTB              string   `json:"dtb,omitempty"`
	BootConfig       string   `json:"boot_config,omitempty"`
	Cmdline          string   `json:"cmdline"`
	Modules          []Module `json:"modules,omitempty"`
}

// Kernel describes where to fetch the kernel image.
type Kernel struct {
	Source  string `json:"source"`
	Package string `json:"package"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// Asset is a downloadable file (firmware, DTB, etc.).
type Asset struct {
	URL    string `json:"url"`
	Dest   string `json:"dest"`
	SHA256 string `json:"sha256"`
}

// Module describes a kernel module to extract from the kernel package
// and load at boot time. Path is relative to the modules directory
// inside the APK (e.g., "kernel/net/core/failover.ko.gz").
type Module struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// Parse decodes a board profile from JSON data and validates required fields.
func Parse(data []byte) (*Board, error) {
	var b Board
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse board profile: %w", err)
	}
	if b.Name == "" {
		return nil, fmt.Errorf("board profile missing name")
	}
	if b.Arch == "" {
		return nil, fmt.Errorf("board profile missing arch")
	}
	return &b, nil
}

// Load reads a board profile from a JSON file.
func Load(path string) (*Board, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read board profile: %w", err)
	}
	return Parse(data)
}

// IsQEMU returns true if this board runs under QEMU (no firmware, no DTB,
// no SD card image — just kernel + initrd).
func (b *Board) IsQEMU() bool {
	return len(b.Firmware) == 0 && b.DTB == "" && b.BootConfig == ""
}
