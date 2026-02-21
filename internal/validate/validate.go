// Package validate checks that a binary is suitable for a wisp target.
// It verifies the binary is a static ELF for the correct architecture
// using only the Go standard library (debug/elf).
package validate

import (
	"debug/elf"
	"fmt"
)

// Binary validates that the file at path is a static ELF binary for the
// given architecture with the correct page alignment. It returns nil if
// the binary is valid, or an error describing the first problem found.
func Binary(path string, arch elf.Machine, pageSize uint64) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("not a valid ELF binary: %w", err)
	}
	defer f.Close()

	// Check architecture.
	if f.Machine != arch {
		return fmt.Errorf("wrong architecture: got %s, want %s", f.Machine, arch)
	}

	// Check static linkage: a dynamically linked binary has a PT_INTERP
	// program header pointing to the dynamic linker (e.g., /lib/ld-linux-aarch64.so.1).
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return fmt.Errorf("binary is dynamically linked (has PT_INTERP)")
		}
	}

	// Check page alignment if required. On Pi 5 (BCM2712), the kernel uses
	// 16KB pages. Binaries compiled with 4KB alignment will segfault.
	// The alignment is determined by the PT_LOAD segments.
	if pageSize > 0 {
		for _, p := range f.Progs {
			if p.Type == elf.PT_LOAD {
				if p.Align < pageSize {
					return fmt.Errorf("page alignment %d is less than required %d (recompile with -Wl,-z,max-page-size=%d)",
						p.Align, pageSize, pageSize)
				}
			}
		}
	}

	return nil
}
