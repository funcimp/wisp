// Package fetch downloads and caches board assets (kernel packages, firmware)
// with optional SHA256 verification. Assets are cached in ~/.cache/wisp/ and
// reused across builds when the version and checksum match.
package fetch

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/funcimp/wisp/internal/board"
)

// CacheDir returns the wisp cache directory (~/.cache/wisp).
func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "wisp"), nil
}

// Download fetches a URL to destPath. If wantSHA256 is non-empty, the download
// is verified against the expected checksum. If destPath already exists with the
// correct checksum, the download is skipped.
func Download(url, destPath, wantSHA256 string) error {
	// Check if cached file already matches.
	if wantSHA256 != "" {
		if ok, _ := checksum(destPath, wantSHA256); ok {
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}

	h := sha256.New()
	w := io.MultiWriter(f, h)

	if _, err := io.Copy(w, resp.Body); err != nil {
		f.Close()
		os.Remove(destPath)
		return fmt.Errorf("download %s: %w", url, err)
	}

	if err := f.Close(); err != nil {
		os.Remove(destPath)
		return err
	}

	if wantSHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != wantSHA256 {
			os.Remove(destPath)
			return fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, got, wantSHA256)
		}
	}

	return nil
}

// ExtractAPK extracts specific files from an Alpine APK package (tar.gz).
// The paths map keys are tar entry paths to match (e.g.,
// "lib/modules/6.18.13-0-virt/kernel/net/core/failover.ko.gz") and values are
// destination file paths. Matched files are written to their destinations.
func ExtractAPK(apkPath string, paths map[string]string) error {
	f, err := os.Open(apkPath)
	if err != nil {
		return fmt.Errorf("open apk: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	found := 0

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		dest, ok := paths[hdr.Name]
		if !ok {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("create dir for %s: %w", dest, err)
		}

		out, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("create %s: %w", dest, err)
		}

		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("extract %s: %w", hdr.Name, err)
		}

		if err := out.Close(); err != nil {
			return err
		}

		found++
		if found == len(paths) {
			break
		}
	}

	if found != len(paths) {
		var missing []string
		for p, dest := range paths {
			if _, err := os.Stat(dest); err != nil {
				missing = append(missing, p)
			}
		}
		return fmt.Errorf("missing files in APK: %s", strings.Join(missing, ", "))
	}

	return nil
}

// DecompressGzip decompresses a gzip file to dest.
func DecompressGzip(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, gz); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// KernelResult holds paths to fetched kernel assets.
type KernelResult struct {
	KernelPath  string   // path to vmlinuz
	ModulePaths []string // paths to decompressed .ko files
}

// kernelVersion returns the Alpine kernel version string used in module paths.
// For package "linux-virt" version "6.18.13-r0", this returns "6.18.13-0-virt".
// For package "linux-rpi" version "6.6.31-r0", this returns "6.6.31-0-rpi".
func kernelVersion(b *board.Board) string {
	ver := b.Kernel.Version
	// Strip the "-rN" suffix and convert to "-N".
	ver = strings.Replace(ver, "-r", "-", 1)
	// Append the kernel flavor (virt, rpi, etc.)
	pkg := b.Kernel.Package
	flavor := strings.TrimPrefix(pkg, "linux-")
	return ver + "-" + flavor
}

// Kernel downloads the kernel package for the given board, extracts the
// kernel image and modules, and returns paths to the extracted files. Results
// are cached under ~/.cache/wisp/<board>/.
func Kernel(b *board.Board) (*KernelResult, error) {
	cacheBase, err := CacheDir()
	if err != nil {
		return nil, err
	}
	boardDir := filepath.Join(cacheBase, b.Name, b.Kernel.Version)

	// Check if already cached.
	kernelPath := filepath.Join(boardDir, "vmlinuz")
	if _, err := os.Stat(kernelPath); err == nil {
		// Verify modules are also present.
		allPresent := true
		var modPaths []string
		for _, m := range b.Modules {
			mp := filepath.Join(boardDir, "modules", m.Name)
			if _, err := os.Stat(mp); err != nil {
				allPresent = false
				break
			}
			modPaths = append(modPaths, mp)
		}
		if allPresent {
			return &KernelResult{KernelPath: kernelPath, ModulePaths: modPaths}, nil
		}
	}

	if err := os.MkdirAll(boardDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	// Construct the APK URL.
	apkURL := fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/%s-%s.apk",
		b.Kernel.Package, b.Kernel.Version)
	apkPath := filepath.Join(boardDir, filepath.Base(apkURL))

	// Download the APK.
	if err := Download(apkURL, apkPath, b.Kernel.SHA256); err != nil {
		return nil, fmt.Errorf("download kernel: %w", err)
	}

	// Determine which files to extract.
	kver := kernelVersion(b)

	// Kernel image path inside the APK. Alpine uses "boot/vmlinuz-<flavor>".
	flavor := strings.TrimPrefix(b.Kernel.Package, "linux-")
	vmlinuzInAPK := "boot/vmlinuz-" + flavor

	extractPaths := map[string]string{
		vmlinuzInAPK: kernelPath,
	}

	// Module paths inside the APK.
	modulesDir := filepath.Join(boardDir, "modules")
	if err := os.MkdirAll(modulesDir, 0755); err != nil {
		return nil, fmt.Errorf("create modules dir: %w", err)
	}

	for _, m := range b.Modules {
		apkModPath := "lib/modules/" + kver + "/" + m.Path
		destPath := filepath.Join(modulesDir, m.Name+".gz")
		extractPaths[apkModPath] = destPath
	}

	// Extract files from APK.
	if err := ExtractAPK(apkPath, extractPaths); err != nil {
		return nil, fmt.Errorf("extract kernel package: %w", err)
	}

	// Decompress .ko.gz modules to .ko.
	var modPaths []string
	for _, m := range b.Modules {
		gzPath := filepath.Join(modulesDir, m.Name+".gz")
		koPath := filepath.Join(modulesDir, m.Name)
		if err := DecompressGzip(gzPath, koPath); err != nil {
			return nil, fmt.Errorf("decompress module %s: %w", m.Name, err)
		}
		os.Remove(gzPath)
		modPaths = append(modPaths, koPath)
	}

	// Clean up the APK.
	os.Remove(apkPath)

	return &KernelResult{KernelPath: kernelPath, ModulePaths: modPaths}, nil
}

// Firmware downloads firmware files for the given board to the cache
// directory and returns a map of destination filename to local path.
func Firmware(b *board.Board) (map[string]string, error) {
	if len(b.Firmware) == 0 {
		return nil, nil
	}

	cacheBase, err := CacheDir()
	if err != nil {
		return nil, err
	}
	fwDir := filepath.Join(cacheBase, b.Name, "firmware")
	if err := os.MkdirAll(fwDir, 0755); err != nil {
		return nil, fmt.Errorf("create firmware dir: %w", err)
	}

	result := make(map[string]string)
	for _, fw := range b.Firmware {
		localPath := filepath.Join(fwDir, fw.Dest)
		if err := Download(fw.URL, localPath, fw.SHA256); err != nil {
			return nil, fmt.Errorf("download firmware %s: %w", fw.Dest, err)
		}
		result[fw.Dest] = localPath
	}

	return result, nil
}

// checksum verifies that the file at path has the expected SHA256 hex digest.
func checksum(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}

	got := hex.EncodeToString(h.Sum(nil))
	return got == want, nil
}
