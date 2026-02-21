package validate

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBinary(t *testing.T) {
	// Build test binaries in a temp dir.
	tmp := t.TempDir()

	// Build a static aarch64 binary (our helloworld).
	staticBin := filepath.Join(tmp, "static-arm64")
	build(t, staticBin, "linux", "arm64", "../../testdata/helloworld")

	// Build a static amd64 binary (wrong arch).
	wrongArchBin := filepath.Join(tmp, "static-amd64")
	build(t, wrongArchBin, "linux", "amd64", "../../testdata/helloworld")

	tests := []struct {
		name     string
		path     string
		arch     elf.Machine
		pageSize uint64
		wantErr  string
	}{
		{
			name: "valid static aarch64 binary",
			path: staticBin,
			arch: elf.EM_AARCH64,
		},
		{
			name:    "wrong architecture",
			path:    wrongArchBin,
			arch:    elf.EM_AARCH64,
			wantErr: "wrong architecture",
		},
		{
			name:    "not an ELF file",
			path:    "validate.go",
			arch:    elf.EM_AARCH64,
			wantErr: "not a valid ELF",
		},
		{
			name:    "nonexistent file",
			path:    "/nonexistent",
			arch:    elf.EM_AARCH64,
			wantErr: "not a valid ELF",
		},
		{
			name:     "page alignment check passes (4KB default)",
			path:     staticBin,
			arch:     elf.EM_AARCH64,
			pageSize: 4096,
		},
		{
			name:     "page alignment check fails (128KB requirement)",
			path:     staticBin,
			arch:     elf.EM_AARCH64,
			pageSize: 131072,
			wantErr:  "page alignment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Binary(tt.path, tt.arch, tt.pageSize)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func build(t *testing.T, output, goos, goarch, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", output, pkg)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
