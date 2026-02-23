// wisp builds bootable images for running a single static binary on
// single-board computers. See DESIGN.md for architecture details.
//
// Usage:
//
//	wisp build --target pi5 --binary ./myservice --ip 192.168.1.100/24 --gateway 192.168.1.1
//	wisp run --target qemu --binary ./myservice --ip 10.0.2.15/24 --gateway 10.0.2.2
//	wisp targets
//	wisp validate --target pi5 --binary ./myservice
package main

import (
	"bytes"
	"debug/elf"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/funcimp/wisp/internal/board"
	"github.com/funcimp/wisp/internal/fetch"
	"github.com/funcimp/wisp/internal/image"
	"github.com/funcimp/wisp/internal/initrd"
	"github.com/funcimp/wisp/internal/validate"
)

//go:embed embed/init-arm64
var initBinary []byte

//go:embed boards
var boardsFS embed.FS

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "build":
		if err := cmdBuild(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "wisp build: %v\n", err)
			os.Exit(1)
		}
	case "run":
		if err := cmdRun(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "wisp run: %v\n", err)
			os.Exit(1)
		}
	case "targets":
		cmdTargets()
	case "validate":
		if err := cmdValidate(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "wisp validate: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "wisp: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: wisp <command> [flags]

Commands:
  build      Build a bootable image
  run        Build and boot in QEMU (qemu target only)
  targets    List supported targets
  validate   Validate a binary for a target
`)
}

// config holds build configuration from flags or a config file.
type config struct {
	Target  string `json:"target"`
	Binary  string `json:"binary"`
	IP      string `json:"ip"`
	Gateway string `json:"gateway"`
	DNS     string `json:"dns"`
	Output  string `json:"output"`
}

func parseFlags(args []string) (*config, error) {
	fs := flag.NewFlagSet("wisp", flag.ContinueOnError)

	var cfg config
	var configFile string

	fs.StringVar(&cfg.Target, "target", "", "board target name (required)")
	fs.StringVar(&cfg.Target, "t", "", "board target name (shorthand)")
	fs.StringVar(&cfg.Binary, "binary", "", "path to static binary (required)")
	fs.StringVar(&cfg.Binary, "b", "", "path to static binary (shorthand)")
	fs.StringVar(&cfg.IP, "ip", "", "IP address with CIDR (required)")
	fs.StringVar(&cfg.Gateway, "gateway", "", "default gateway (required)")
	fs.StringVar(&cfg.DNS, "dns", "", "DNS server (default: gateway address)")
	fs.StringVar(&cfg.Output, "output", "wisp-output", "output directory")
	fs.StringVar(&cfg.Output, "o", "wisp-output", "output directory (shorthand)")
	fs.StringVar(&configFile, "f", "", "config file (alternative to flags)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// If config file specified, load it.
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("read config file: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse config file: %w", err)
		}
	}

	// Validate required fields.
	if cfg.Target == "" {
		return nil, fmt.Errorf("--target is required")
	}
	if cfg.Binary == "" {
		return nil, fmt.Errorf("--binary is required")
	}
	if cfg.IP == "" {
		return nil, fmt.Errorf("--ip is required")
	}
	if cfg.Gateway == "" {
		return nil, fmt.Errorf("--gateway is required")
	}
	if cfg.DNS == "" {
		cfg.DNS = cfg.Gateway
	}

	return &cfg, nil
}

// loadBoard loads a board profile from the embedded filesystem.
func loadBoard(name string) (*board.Board, error) {
	data, err := boardsFS.ReadFile("boards/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("unknown target %q", name)
	}
	return board.Parse(data)
}

// archToELF maps board architecture strings to ELF machine types.
func archToELF(arch string) elf.Machine {
	switch arch {
	case "aarch64":
		return elf.EM_AARCH64
	default:
		return elf.EM_NONE
	}
}

func cmdBuild(args []string) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}

	b, err := loadBoard(cfg.Target)
	if err != nil {
		return err
	}

	return build(cfg, b)
}

func build(cfg *config, b *board.Board) error {
	// Step 1: Validate binary.
	fmt.Fprintf(os.Stderr, "validating binary %s\n", cfg.Binary)
	if err := validate.Binary(cfg.Binary, archToELF(b.Arch), b.PageSize); err != nil {
		return fmt.Errorf("validate binary: %w", err)
	}

	// Step 2: Fetch kernel and modules.
	fmt.Fprintf(os.Stderr, "fetching kernel (%s %s)\n", b.Kernel.Package, b.Kernel.Version)
	kr, err := fetch.Kernel(b)
	if err != nil {
		return fmt.Errorf("fetch kernel: %w", err)
	}

	// Step 3: Fetch firmware (hardware targets only).
	var fwPaths map[string]string
	if !b.IsQEMU() {
		fmt.Fprintf(os.Stderr, "fetching firmware\n")
		fwPaths, err = fetch.Firmware(b)
		if err != nil {
			return fmt.Errorf("fetch firmware: %w", err)
		}
	}

	// Step 4: Build initrd.
	fmt.Fprintf(os.Stderr, "building initrd\n")
	initrdData, err := buildInitrd(cfg, b, kr)
	if err != nil {
		return fmt.Errorf("build initrd: %w", err)
	}

	// Step 5: Create output.
	if err := os.MkdirAll(cfg.Output, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	if b.IsQEMU() {
		return outputQEMU(cfg, kr, initrdData)
	}
	return outputImage(cfg, b, kr, fwPaths, initrdData)
}

// buildInitrd assembles the initrd cpio archive.
func buildInitrd(cfg *config, b *board.Board, kr *fetch.KernelResult) ([]byte, error) {
	var entries []initrd.Entry

	// Mount-point directories.
	for _, dir := range []string{"dev", "proc", "sys"} {
		entries = append(entries, initrd.Entry{Path: dir, Mode: os.ModeDir | 0755})
	}

	// Init binary (embedded in wisp).
	entries = append(entries, initrd.Entry{Path: "init", Data: initBinary, Mode: 0755})

	// Service binary.
	serviceData, err := os.ReadFile(cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("read binary: %w", err)
	}
	entries = append(entries, initrd.Entry{Path: "service/run", Data: serviceData, Mode: 0755})

	// Network config.
	iface := b.NetworkInterface
	wispConf := fmt.Sprintf("IFACE=%s\nADDR=%s\nGW=%s\n", iface, cfg.IP, cfg.Gateway)
	entries = append(entries, initrd.Entry{Path: "etc/wisp.conf", Data: []byte(wispConf), Mode: 0644})

	// DNS config.
	resolvConf := fmt.Sprintf("nameserver %s\n", cfg.DNS)
	entries = append(entries, initrd.Entry{Path: "etc/resolv.conf", Data: []byte(resolvConf), Mode: 0644})

	// Module list and module files.
	if len(kr.ModulePaths) > 0 {
		var moduleNames []string
		for _, mp := range kr.ModulePaths {
			name := filepath.Base(mp)
			moduleNames = append(moduleNames, name)

			data, err := os.ReadFile(mp)
			if err != nil {
				return nil, fmt.Errorf("read module %s: %w", name, err)
			}
			entries = append(entries, initrd.Entry{
				Path: "lib/modules/" + name,
				Data: data,
				Mode: 0644,
			})
		}
		modulesList := strings.Join(moduleNames, "\n") + "\n"
		entries = append(entries, initrd.Entry{Path: "etc/modules", Data: []byte(modulesList), Mode: 0644})
	}

	// Write to buffer.
	var buf bytes.Buffer
	if err := initrd.Write(&buf, entries); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// outputQEMU writes kernel and initrd to the output directory.
func outputQEMU(cfg *config, kr *fetch.KernelResult, initrdData []byte) error {
	// Copy kernel.
	kernelData, err := os.ReadFile(kr.KernelPath)
	if err != nil {
		return fmt.Errorf("read kernel: %w", err)
	}
	kernelPath := filepath.Join(cfg.Output, "vmlinuz")
	if err := os.WriteFile(kernelPath, kernelData, 0644); err != nil {
		return err
	}

	// Write initrd.
	initrdPath := filepath.Join(cfg.Output, "initrd.img")
	if err := os.WriteFile(initrdPath, initrdData, 0644); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "output: %s/vmlinuz, %s/initrd.img\n", cfg.Output, cfg.Output)
	return nil
}

// outputImage builds a FAT32 disk image for hardware targets.
func outputImage(cfg *config, b *board.Board, kr *fetch.KernelResult, fwPaths map[string]string, initrdData []byte) error {
	var files []image.File

	// Kernel image.
	kernelData, err := os.ReadFile(kr.KernelPath)
	if err != nil {
		return fmt.Errorf("read kernel: %w", err)
	}
	files = append(files, image.File{Name: "Image", Data: kernelData})

	// Initrd.
	files = append(files, image.File{Name: "initrd.img", Data: initrdData})

	// Firmware files.
	for dest, localPath := range fwPaths {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return fmt.Errorf("read firmware %s: %w", dest, err)
		}
		files = append(files, image.File{Name: dest, Data: data})
	}

	// DTB (fetched as part of the kernel package for Alpine).
	// For now, DTB handling is a TODO — it comes from the kernel APK
	// or firmware repo depending on the board.

	// Boot config (config.txt).
	if b.BootConfig != "" {
		files = append(files, image.File{Name: "config.txt", Data: []byte(b.BootConfig)})
	}

	// Kernel command line (cmdline.txt).
	if b.Cmdline != "" {
		files = append(files, image.File{Name: "cmdline.txt", Data: []byte(b.Cmdline + "\n")})
	}

	imgPath := filepath.Join(cfg.Output, b.Name+".img")
	if err := image.Build(imgPath, files); err != nil {
		return fmt.Errorf("build image: %w", err)
	}

	fmt.Fprintf(os.Stderr, "output: %s\n", imgPath)
	return nil
}

func cmdRun(args []string) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}

	b, err := loadBoard(cfg.Target)
	if err != nil {
		return err
	}

	if !b.IsQEMU() {
		return fmt.Errorf("run is only supported for QEMU targets (got %q)", cfg.Target)
	}

	// Build first.
	if err := build(cfg, b); err != nil {
		return err
	}

	// Launch QEMU.
	kernelPath := filepath.Join(cfg.Output, "vmlinuz")
	initrdPath := filepath.Join(cfg.Output, "initrd.img")

	port := "18080"
	fmt.Fprintf(os.Stderr, "booting QEMU (port forward: localhost:%s -> guest:8080)\n", port)
	fmt.Fprintf(os.Stderr, "test: curl http://localhost:%s/\n", port)
	fmt.Fprintf(os.Stderr, "quit: Ctrl-A X\n")
	fmt.Fprintf(os.Stderr, "---\n")

	cmd := exec.Command("qemu-system-aarch64",
		"-machine", "virt",
		"-accel", "hvf",
		"-cpu", "host",
		"-m", "512M",
		"-kernel", kernelPath,
		"-initrd", initrdPath,
		"-append", b.Cmdline,
		"-nographic",
		"-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp::%s-:8080", port),
		"-device", "virtio-net-pci,netdev=net0",
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func cmdTargets() {
	entries, err := boardsFS.ReadDir("boards")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wisp targets: %v\n", err)
		os.Exit(1)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		data, err := boardsFS.ReadFile("boards/" + e.Name())
		if err != nil {
			continue
		}
		b, err := board.Parse(data)
		if err != nil {
			continue
		}
		label := name
		if b.IsQEMU() {
			label += " (QEMU)"
		}
		fmt.Println(label)
	}
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)

	var target, binary string
	fs.StringVar(&target, "target", "", "board target name (required)")
	fs.StringVar(&target, "t", "", "board target name (shorthand)")
	fs.StringVar(&binary, "binary", "", "path to static binary (required)")
	fs.StringVar(&binary, "b", "", "path to static binary (shorthand)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if target == "" {
		return fmt.Errorf("--target is required")
	}
	if binary == "" {
		return fmt.Errorf("--binary is required")
	}

	b, err := loadBoard(target)
	if err != nil {
		return err
	}

	if err := validate.Binary(binary, archToELF(b.Arch), b.PageSize); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "ok: %s is valid for %s\n", binary, target)
	return nil
}
