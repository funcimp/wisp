package fetch

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/funcimp/wisp/internal/board"
)

func TestDownload(t *testing.T) {
	content := []byte("hello wisp kernel")
	h := sha256.Sum256(content)
	wantHash := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		sha256  string
		wantErr bool
	}{
		{
			name:   "download with valid checksum",
			sha256: wantHash,
		},
		{
			name:   "download without checksum",
			sha256: "",
		},
		{
			name:    "download with wrong checksum",
			sha256:  "0000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatalf("open root: %v", err)
			}
			defer root.Close()

			err = download(root, srv.URL+"/kernel.apk", "downloaded", tt.sha256)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, err := root.ReadFile("downloaded")
			if err != nil {
				t.Fatalf("read downloaded file: %v", err)
			}
			if string(got) != string(content) {
				t.Fatalf("content mismatch: got %q, want %q", got, content)
			}
		})
	}
}

func TestDownloadCached(t *testing.T) {
	content := []byte("cached content")
	h := sha256.Sum256(content)
	wantHash := hex.EncodeToString(h[:])

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write(content)
	}))
	defer srv.Close()

	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	// First download.
	if err := download(root, srv.URL+"/file", "cached", wantHash); err != nil {
		t.Fatalf("first download: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", calls)
	}

	// Second download should use cache (checksum matches).
	if err := download(root, srv.URL+"/file", "cached", wantHash); err != nil {
		t.Fatalf("second download: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 HTTP call (cached), got %d", calls)
	}
}

func TestExtractAPK(t *testing.T) {
	// Build a tar.gz in memory with a few test files.
	tmpDir := t.TempDir()
	apkPath := tmpDir + "/test.apk"

	files := map[string]string{
		"boot/vmlinuz-virt":                                              "kernel-image-data",
		"lib/modules/6.18.13-0-virt/kernel/net/core/failover.ko.gz":     "failover-module",
		"lib/modules/6.18.13-0-virt/kernel/drivers/net/virtio_net.ko.gz": "virtio-module",
		".PKGINFO": "pkgname=linux-virt",
	}

	if err := writeTarGz(apkPath, files); err != nil {
		t.Fatalf("create test APK: %v", err)
	}

	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	// Extract specific files.
	paths := map[string]string{
		"boot/vmlinuz-virt": "vmlinuz",
		"lib/modules/6.18.13-0-virt/kernel/net/core/failover.ko.gz": "failover.ko.gz",
	}

	if err := extractAPK(root, "test.apk", paths); err != nil {
		t.Fatalf("extract: %v", err)
	}

	// Verify extracted files.
	got, err := root.ReadFile("vmlinuz")
	if err != nil {
		t.Fatalf("read kernel: %v", err)
	}
	if string(got) != "kernel-image-data" {
		t.Fatalf("kernel content: got %q, want %q", got, "kernel-image-data")
	}

	got, err = root.ReadFile("failover.ko.gz")
	if err != nil {
		t.Fatalf("read module: %v", err)
	}
	if string(got) != "failover-module" {
		t.Fatalf("module content: got %q, want %q", got, "failover-module")
	}
}

func TestExtractAPKMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := tmpDir + "/test.apk"

	files := map[string]string{
		"boot/vmlinuz-virt": "kernel",
	}
	if err := writeTarGz(apkPath, files); err != nil {
		t.Fatalf("create test APK: %v", err)
	}

	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	paths := map[string]string{
		"boot/vmlinuz-virt": "vmlinuz",
		"missing/file":      "missing",
	}

	err = extractAPK(root, "test.apk", paths)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestKernelVersion(t *testing.T) {
	tests := []struct {
		pkg     string
		version string
		want    string
	}{
		{"linux-virt", "6.18.13-r0", "6.18.13-0-virt"},
		{"linux-rpi", "6.6.31-r0", "6.6.31-0-rpi"},
		{"linux-virt", "6.12.1-r2", "6.12.1-2-virt"},
	}

	for _, tt := range tests {
		t.Run(tt.pkg+"/"+tt.version, func(t *testing.T) {
			b := &board.Board{
				Kernel: board.Kernel{
					Package: tt.pkg,
					Version: tt.version,
				},
			}
			got := kernelVersion(b)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// writeTarGz creates a tar.gz file at path with the given file contents.
func writeTarGz(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
