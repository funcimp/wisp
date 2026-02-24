// Package board loads and represents board profiles. A board profile is a
// JSON file that defines everything board-specific: kernel source, firmware
// URLs, required modules, network interface, and boot configuration.
package board

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

//go:embed boards
var boardsFS embed.FS

// Board defines a target board's hardware profile.
type Board struct {
	Name             string      `json:"name"`
	Arch             string      `json:"arch"`
	PageSize         uint64      `json:"page_size"`
	NetworkInterface string      `json:"network_interface"`
	Kernel           Kernel      `json:"kernel"`
	Firmware         []Asset     `json:"firmware,omitempty"`
	DTB              string      `json:"dtb,omitempty"`
	BootConfig       string      `json:"boot_config,omitempty"`
	Cmdline          string      `json:"cmdline"`
	Modules          []Module    `json:"modules,omitempty"`
	QEMU             *QEMUConfig `json:"qemu,omitempty"`
}

// QEMUConfig defines QEMU emulation parameters for a board target.
type QEMUConfig struct {
	Binary  string   `json:"binary"`
	Machine string   `json:"machine"`
	CPU     string   `json:"cpu,omitempty"`
	Memory  string   `json:"memory"`
	Accel   string   `json:"accel,omitempty"`
	NetDev  string   `json:"net_dev,omitempty"`
	Extra   []string `json:"extra,omitempty"`
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

// Load reads a board profile from a JSON file on disk.
func Load(path string) (*Board, error) {
	data, err := os.ReadFile(path) //#nosec G304 -- user-provided board profile path
	if err != nil {
		return nil, fmt.Errorf("read board profile: %w", err)
	}
	return Parse(data)
}

// Get loads a built-in board profile by name from the embedded board
// definitions.
func Get(name string) (*Board, error) {
	data, err := boardsFS.ReadFile("boards/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("unknown target %q", name)
	}
	return Parse(data)
}

// List returns all built-in board profiles, sorted by embedded directory
// order.
func List() ([]*Board, error) {
	entries, err := boardsFS.ReadDir("boards")
	if err != nil {
		return nil, fmt.Errorf("read embedded boards: %w", err)
	}

	var boards []*Board
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := boardsFS.ReadFile("boards/" + e.Name())
		if err != nil {
			continue
		}
		b, err := Parse(data)
		if err != nil {
			continue
		}
		boards = append(boards, b)
	}
	return boards, nil
}

// IsQEMU returns true if this board has QEMU emulation configuration.
func (b *Board) IsQEMU() bool {
	return b.QEMU != nil
}
