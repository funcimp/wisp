// mkinitrd assembles a wisp initrd image using the internal/initrd package.
// It reads file paths from flags, builds the cpio archive in pure Go, and
// writes the gzipped result to stdout or a file.
//
// Usage:
//
//	mkinitrd -o initrd.img \
//	  -init ./build/init \
//	  -service ./build/helloworld \
//	  -conf ./build/initrd/etc/wisp.conf \
//	  -resolv ./build/initrd/etc/resolv.conf \
//	  -modules-list ./build/initrd/etc/modules \
//	  -modules-dir ./build/modules
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/funcimp/wisp/internal/initrd"
)

func main() {
	var (
		output      = flag.String("o", "", "output file (required)")
		initBin     = flag.String("init", "", "path to init binary (required)")
		serviceBin  = flag.String("service", "", "path to service binary (required)")
		confFile    = flag.String("conf", "", "path to wisp.conf (required)")
		resolvFile  = flag.String("resolv", "", "path to resolv.conf (required)")
		modulesList = flag.String("modules-list", "", "path to modules list file (optional)")
		modulesDir  = flag.String("modules-dir", "", "directory containing .ko files (optional)")
	)
	flag.Parse()

	if *output == "" || *initBin == "" || *serviceBin == "" || *confFile == "" || *resolvFile == "" {
		flag.Usage()
		os.Exit(1)
	}

	entries, err := buildEntries(*initBin, *serviceBin, *confFile, *resolvFile, *modulesList, *modulesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkinitrd: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Create(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkinitrd: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	if err := initrd.Write(f, entries); err != nil {
		os.Remove(*output)
		fmt.Fprintf(os.Stderr, "mkinitrd: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "mkinitrd: wrote %s\n", *output)
}

func buildEntries(initBin, serviceBin, confFile, resolvFile, modulesList, modulesDir string) ([]initrd.Entry, error) {
	var entries []initrd.Entry

	// Empty mount-point directories.
	for _, dir := range []string{"dev", "proc", "sys"} {
		entries = append(entries, initrd.Entry{Path: dir, Mode: os.ModeDir | 0755})
	}

	// Init binary.
	data, err := os.ReadFile(initBin)
	if err != nil {
		return nil, fmt.Errorf("read init: %w", err)
	}
	entries = append(entries, initrd.Entry{Path: "init", Data: data, Mode: 0755})

	// Service binary.
	data, err = os.ReadFile(serviceBin)
	if err != nil {
		return nil, fmt.Errorf("read service: %w", err)
	}
	entries = append(entries, initrd.Entry{Path: "service/run", Data: data, Mode: 0755})

	// Config files.
	data, err = os.ReadFile(confFile)
	if err != nil {
		return nil, fmt.Errorf("read conf: %w", err)
	}
	entries = append(entries, initrd.Entry{Path: "etc/wisp.conf", Data: data, Mode: 0644})

	data, err = os.ReadFile(resolvFile)
	if err != nil {
		return nil, fmt.Errorf("read resolv: %w", err)
	}
	entries = append(entries, initrd.Entry{Path: "etc/resolv.conf", Data: data, Mode: 0644})

	// Module list (optional).
	if modulesList != "" {
		data, err = os.ReadFile(modulesList)
		if err != nil {
			return nil, fmt.Errorf("read modules list: %w", err)
		}
		entries = append(entries, initrd.Entry{Path: "etc/modules", Data: data, Mode: 0644})
	}

	// Kernel modules (optional).
	if modulesDir != "" {
		mods, err := filepath.Glob(filepath.Join(modulesDir, "*.ko"))
		if err != nil {
			return nil, fmt.Errorf("glob modules: %w", err)
		}
		for _, mod := range mods {
			data, err = os.ReadFile(mod)
			if err != nil {
				return nil, fmt.Errorf("read module %s: %w", mod, err)
			}
			entries = append(entries, initrd.Entry{
				Path: "lib/modules/" + filepath.Base(mod),
				Data: data,
				Mode: 0644,
			})
		}
	}

	return entries, nil
}
