package initrd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NetworkConfig holds the network parameters baked into the initrd.
type NetworkConfig struct {
	Interface string // network interface name, e.g. "eth0"
	Address   string // IP address with CIDR, e.g. "192.168.1.100/24"
	Gateway   string // default gateway, e.g. "192.168.1.1"
	DNS       string // DNS server, e.g. "192.168.1.1"
}

// KernelModule identifies a kernel module to include in the initrd.
type KernelModule struct {
	HostPath string // absolute path to the .ko file on the build host
}

// Build assembles a complete initrd image. It embeds the init binary for the
// given architecture, reads the service binary and kernel modules from disk,
// generates configuration files, and returns the gzipped cpio archive.
func Build(arch, servicePath string, net NetworkConfig, modules []KernelModule) ([]byte, error) {
	initData, err := InitBinary(arch)
	if err != nil {
		return nil, fmt.Errorf("init binary: %w", err)
	}

	serviceData, err := os.ReadFile(servicePath) //#nosec G304 -- user-provided binary path
	if err != nil {
		return nil, fmt.Errorf("read binary: %w", err)
	}

	var moduleData []moduleFile
	for _, m := range modules {
		data, err := os.ReadFile(m.HostPath)
		if err != nil {
			name := filepath.Base(m.HostPath)
			return nil, fmt.Errorf("read module %s: %w", name, err)
		}
		moduleData = append(moduleData, moduleFile{
			name: filepath.Base(m.HostPath),
			data: data,
		})
	}

	entries := buildEntries(initData, serviceData, net, moduleData)

	var buf bytes.Buffer
	if err := Write(&buf, entries); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// moduleFile holds a kernel module's filename and contents for entry assembly.
type moduleFile struct {
	name string
	data []byte
}

// buildEntries assembles the initrd entry list from in-memory data. This is
// separated from Build so it can be tested without embedded binaries or disk
// I/O.
func buildEntries(initData, serviceData []byte, net NetworkConfig, modules []moduleFile) []Entry {
	var entries []Entry

	// Mount-point directories.
	for _, dir := range []string{"dev", "proc", "sys"} {
		entries = append(entries, Entry{Path: dir, Mode: os.ModeDir | 0755})
	}

	// Init binary.
	entries = append(entries, Entry{Path: "init", Data: initData, Mode: 0755})

	// Service binary.
	entries = append(entries, Entry{Path: "service/run", Data: serviceData, Mode: 0755})

	// Network config.
	wispConf := fmt.Sprintf("IFACE=%s\nADDR=%s\nGW=%s\n", net.Interface, net.Address, net.Gateway)
	entries = append(entries, Entry{Path: "etc/wisp.conf", Data: []byte(wispConf), Mode: 0644})

	// DNS config.
	resolvConf := fmt.Sprintf("nameserver %s\n", net.DNS)
	entries = append(entries, Entry{Path: "etc/resolv.conf", Data: []byte(resolvConf), Mode: 0644})

	// Kernel modules.
	if len(modules) > 0 {
		var moduleNames []string
		for _, m := range modules {
			moduleNames = append(moduleNames, m.name)
			entries = append(entries, Entry{
				Path: "lib/modules/" + m.name,
				Data: m.data,
				Mode: 0644,
			})
		}
		modulesList := strings.Join(moduleNames, "\n") + "\n"
		entries = append(entries, Entry{Path: "etc/modules", Data: []byte(modulesList), Mode: 0644})
	}

	return entries
}
