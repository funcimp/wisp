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
	"debug/elf"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/funcimp/wisp/internal/board"
	"github.com/funcimp/wisp/internal/fetch"
	"github.com/funcimp/wisp/internal/image"
	"github.com/funcimp/wisp/internal/initrd"
	"github.com/funcimp/wisp/internal/validate"
)

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
		fmt.Fprintf(os.Stderr, "wisp: unknown command %q\n", os.Args[1]) //#nosec G705 -- CLI stderr output, %q escapes special characters
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
		data, err := os.ReadFile(configFile) //#nosec G304 -- user-provided config file path
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

// archToELF maps board architecture strings to ELF machine types.
func archToELF(arch string) elf.Machine {
	switch arch {
	case "aarch64":
		return elf.EM_AARCH64
	case "riscv64":
		return elf.EM_RISCV
	case "x86_64":
		return elf.EM_X86_64
	default:
		return elf.EM_NONE
	}
}

func cmdBuild(args []string) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}

	b, err := board.Get(cfg.Target)
	if err != nil {
		return err
	}

	return build(cfg, b)
}

func build(cfg *config, b *board.Board) error {
	// Step 1: Validate binary.
	fmt.Fprintf(os.Stderr, "validating binary %s\n", cfg.Binary) //#nosec G705 -- CLI stderr output
	if err := validate.Binary(cfg.Binary, archToELF(b.Arch), b.PageSize); err != nil {
		return fmt.Errorf("validate binary: %w", err)
	}

	// Step 2: Fetch kernel and modules.
	fmt.Fprintf(os.Stderr, "fetching kernel (%s %s)\n", b.Kernel.Package, b.Kernel.Version) //#nosec G705 -- CLI stderr output
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
	net := initrd.NetworkConfig{
		Interface: b.NetworkInterface,
		Address:   cfg.IP,
		Gateway:   cfg.Gateway,
		DNS:       cfg.DNS,
	}
	var modules []initrd.KernelModule
	for _, mp := range kr.ModulePaths {
		modules = append(modules, initrd.KernelModule{HostPath: mp})
	}
	initrdData, err := initrd.Build(b.Arch, cfg.Binary, net, modules)
	if err != nil {
		return fmt.Errorf("build initrd: %w", err)
	}

	// Step 5: Create output directory scoped to the working directory.
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	wdRoot, err := os.OpenRoot(wd)
	if err != nil {
		return fmt.Errorf("open working directory: %w", err)
	}
	defer wdRoot.Close()

	if err := wdRoot.MkdirAll(cfg.Output, 0750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	outRoot, err := wdRoot.OpenRoot(cfg.Output)
	if err != nil {
		return fmt.Errorf("open output dir: %w", err)
	}
	defer outRoot.Close()

	if b.IsQEMU() {
		return outputQEMU(outRoot, cfg.Output, kr, initrdData)
	}
	return outputImage(outRoot, cfg.Output, b, kr, fwPaths, initrdData)
}

// readCacheFile reads a file from the wisp cache directory, using os.Root
// to scope access and prevent directory traversal.
func readCacheFile(absPath string) ([]byte, error) {
	cacheDir, err := fetch.CacheDir()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	rel, err := filepath.Rel(cacheDir, absPath)
	if err != nil {
		return nil, fmt.Errorf("path not under cache: %w", err)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// outputQEMU writes kernel and initrd to the output directory.
func outputQEMU(root *os.Root, outDir string, kr *fetch.KernelResult, initrdData []byte) error {
	kernelData, err := readCacheFile(kr.KernelPath)
	if err != nil {
		return fmt.Errorf("read kernel: %w", err)
	}
	if err := root.WriteFile("vmlinuz", kernelData, 0600); err != nil {
		return err
	}
	if err := root.WriteFile("initrd.img", initrdData, 0600); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "output: %s/vmlinuz, %s/initrd.img\n", outDir, outDir) //#nosec G705 -- CLI stderr output
	return nil
}

// outputImage builds a FAT32 disk image for hardware targets.
func outputImage(root *os.Root, outDir string, b *board.Board, kr *fetch.KernelResult, fwPaths map[string]string, initrdData []byte) error {
	var files []image.File

	// Kernel image.
	kernelData, err := readCacheFile(kr.KernelPath)
	if err != nil {
		return fmt.Errorf("read kernel: %w", err)
	}
	files = append(files, image.File{Name: "Image", Data: kernelData})

	// Initrd.
	files = append(files, image.File{Name: "initrd.img", Data: initrdData})

	// Firmware files.
	for dest, localPath := range fwPaths {
		data, err := readCacheFile(localPath)
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

	imgName := b.Name + ".img"
	imgData, err := image.Build(files)
	if err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	if err := root.WriteFile(imgName, imgData, 0600); err != nil {
		return fmt.Errorf("write image: %w", err)
	}

	fmt.Fprintf(os.Stderr, "output: %s\n", filepath.Join(outDir, imgName)) //#nosec G705 -- CLI stderr output
	return nil
}

func cmdRun(args []string) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}

	b, err := board.Get(cfg.Target)
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

	// Print the QEMU command for the user to execute.
	kernelPath := filepath.Join(cfg.Output, "vmlinuz")
	initrdPath := filepath.Join(cfg.Output, "initrd.img")
	port := "18080"

	fmt.Println(qemuCommand(b, kernelPath, initrdPath, port))
	return nil
}

// qemuCommand builds a copy-pasteable QEMU command line from a board's
// QEMU configuration.
func qemuCommand(b *board.Board, kernelPath, initrdPath, port string) string {
	q := b.QEMU
	args := []string{
		q.Binary,
		"-machine", q.Machine,
		"-m", q.Memory,
		"-kernel", kernelPath,
		"-initrd", initrdPath,
		"-append", shellQuote(b.Cmdline),
		"-nographic",
	}
	if q.CPU != "" {
		args = append(args, "-cpu", q.CPU)
	}
	if q.Accel != "" {
		args = append(args, "-accel", q.Accel)
	}
	if q.NetDev != "" {
		args = append(args, "-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp::%s-:8080", port))
		args = append(args, "-device", q.NetDev+",netdev=net0")
	}
	args = append(args, q.Extra...)
	return strings.Join(args, " \\\n  ")
}

// shellQuote wraps s in single quotes for safe shell use.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func cmdTargets() {
	boards, err := board.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wisp targets: %v\n", err)
		os.Exit(1)
	}

	for _, b := range boards {
		label := b.Name
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

	b, err := board.Get(target)
	if err != nil {
		return err
	}

	if err := validate.Binary(binary, archToELF(b.Arch), b.PageSize); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "ok: %s is valid for %s\n", binary, target)
	return nil
}
