package initrd

import (
	"embed"
	"fmt"
)

// Embedded init binaries for each supported architecture. The embed directive
// lists files explicitly so that go build fails fast if any binary is missing
// (forces make to run first).
//
//go:embed embed/init-arm64 embed/init-riscv64 embed/init-amd64
var initFS embed.FS

// InitBinary returns the embedded init binary for the given board architecture
// string (e.g., "aarch64", "riscv64", "x86_64"). Callers use the board profile
// arch value directly — the GOARCH mapping is handled internally.
func InitBinary(arch string) ([]byte, error) {
	ga, err := goarch(arch)
	if err != nil {
		return nil, err
	}
	data, err := initFS.ReadFile("embed/init-" + ga)
	if err != nil {
		return nil, fmt.Errorf("read embedded init binary for %s: %w", arch, err)
	}
	return data, nil
}

// goarch maps board architecture strings to Go architecture names used in the
// embedded binary filenames.
func goarch(boardArch string) (string, error) {
	switch boardArch {
	case "aarch64":
		return "arm64", nil
	case "riscv64":
		return "riscv64", nil
	case "x86_64":
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %q", boardArch)
	}
}
