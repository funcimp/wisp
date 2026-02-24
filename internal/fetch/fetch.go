// Package fetch downloads and caches board assets (kernel packages, firmware)
// with optional SHA256 verification. Assets are cached in ~/.cache/wisp/ and
// reused across builds when the version and checksum match.
//
// All file operations within the cache directory use os.Root to scope access
// and prevent directory traversal.
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

// openCacheRoot creates the cache directory if needed and returns an os.Root
// scoped to it, along with the absolute path for constructing return values.
func openCacheRoot() (*os.Root, string, error) {
	cacheDir, err := CacheDir()
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(cacheDir, 0750); err != nil {
		return nil, "", fmt.Errorf("create cache dir: %w", err)
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return nil, "", fmt.Errorf("open cache root: %w", err)
	}
	return root, cacheDir, nil
}

// download fetches a URL to relPath within root. If wantSHA256 is non-empty,
// the download is verified against the expected checksum. If the file already
// exists with the correct checksum, the download is skipped.
func download(root *os.Root, url, relPath, wantSHA256 string) error {
	// Check if cached file already matches.
	if wantSHA256 != "" {
		if ok, _ := checksumFile(root, relPath, wantSHA256); ok {
			return nil
		}
	}

	if err := root.MkdirAll(filepath.Dir(relPath), 0750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	resp, err := http.Get(url) //#nosec G107 -- URLs come from embedded board profiles, not user input
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := root.Create(relPath)
	if err != nil {
		return err
	}

	h := sha256.New()
	w := io.MultiWriter(f, h)

	if _, err := io.Copy(w, resp.Body); err != nil {
		_ = f.Close()
		_ = root.Remove(relPath)
		return fmt.Errorf("download %s: %w", url, err)
	}

	if err := f.Close(); err != nil {
		_ = root.Remove(relPath)
		return err
	}

	if wantSHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != wantSHA256 {
			_ = root.Remove(relPath)
			return fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, got, wantSHA256)
		}
	}

	return nil
}

// extractAPK extracts specific files from an Alpine APK package (tar.gz).
// apkRelPath is the APK location relative to root. The paths map keys are tar
// entry paths to match and values are destination paths relative to root.
func extractAPK(root *os.Root, apkRelPath string, paths map[string]string) error {
	f, err := root.Open(apkRelPath)
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

		if err := root.MkdirAll(filepath.Dir(dest), 0750); err != nil {
			return fmt.Errorf("create dir for %s: %w", dest, err)
		}

		out, err := root.Create(dest)
		if err != nil {
			return fmt.Errorf("create %s: %w", dest, err)
		}

		if _, err := io.Copy(out, tr); err != nil { //#nosec G110 -- decompressing SHA256-verified Alpine packages
			_ = out.Close()
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
			if _, err := root.Stat(dest); err != nil {
				missing = append(missing, p)
			}
		}
		return fmt.Errorf("missing files in APK: %s", strings.Join(missing, ", "))
	}

	return nil
}

// decompressGzip decompresses a gzip file within root.
func decompressGzip(root *os.Root, srcRel, destRel string) error {
	f, err := root.Open(srcRel)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	out, err := root.Create(destRel)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, gz); err != nil { //#nosec G110 -- decompressing SHA256-verified Alpine packages
		_ = out.Close()
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
	root, cacheDir, err := openCacheRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	boardDir := filepath.Join(b.Name, b.Kernel.Version)

	// Check if already cached.
	kernelRel := filepath.Join(boardDir, "vmlinuz")
	if _, err := root.Stat(kernelRel); err == nil {
		allPresent := true
		var modPaths []string
		for _, m := range b.Modules {
			modRel := filepath.Join(boardDir, "modules", m.Name)
			if _, err := root.Stat(modRel); err != nil {
				allPresent = false
				break
			}
			modPaths = append(modPaths, filepath.Join(cacheDir, modRel))
		}
		if allPresent {
			return &KernelResult{
				KernelPath:  filepath.Join(cacheDir, kernelRel),
				ModulePaths: modPaths,
			}, nil
		}
	}

	if err := root.MkdirAll(boardDir, 0750); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	// Construct the APK URL.
	apkURL := fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/v3.23/main/%s/%s-%s.apk",
		alpineArch(b.Arch), b.Kernel.Package, b.Kernel.Version)
	apkRel := filepath.Join(boardDir, filepath.Base(apkURL))

	// Download the APK.
	if err := download(root, apkURL, apkRel, b.Kernel.SHA256); err != nil {
		return nil, fmt.Errorf("download kernel: %w", err)
	}

	// Determine which files to extract.
	kver := kernelVersion(b)

	// Kernel image path inside the APK. Alpine uses "boot/vmlinuz-<flavor>".
	flavor := strings.TrimPrefix(b.Kernel.Package, "linux-")
	vmlinuzInAPK := "boot/vmlinuz-" + flavor

	extractPaths := map[string]string{
		vmlinuzInAPK: kernelRel,
	}

	// Module paths inside the APK.
	modulesRel := filepath.Join(boardDir, "modules")
	if err := root.MkdirAll(modulesRel, 0750); err != nil {
		return nil, fmt.Errorf("create modules dir: %w", err)
	}

	for _, m := range b.Modules {
		apkModPath := "lib/modules/" + kver + "/" + m.Path
		destRel := filepath.Join(modulesRel, m.Name+".gz")
		extractPaths[apkModPath] = destRel
	}

	// Extract files from APK.
	if err := extractAPK(root, apkRel, extractPaths); err != nil {
		return nil, fmt.Errorf("extract kernel package: %w", err)
	}

	// Decompress .ko.gz modules to .ko.
	var modPaths []string
	for _, m := range b.Modules {
		gzRel := filepath.Join(modulesRel, m.Name+".gz")
		koRel := filepath.Join(modulesRel, m.Name)
		if err := decompressGzip(root, gzRel, koRel); err != nil {
			return nil, fmt.Errorf("decompress module %s: %w", m.Name, err)
		}
		_ = root.Remove(gzRel)
		modPaths = append(modPaths, filepath.Join(cacheDir, koRel))
	}

	// Clean up the APK.
	_ = root.Remove(apkRel)

	return &KernelResult{
		KernelPath:  filepath.Join(cacheDir, kernelRel),
		ModulePaths: modPaths,
	}, nil
}

// Firmware downloads firmware files for the given board to the cache
// directory and returns a map of destination filename to local path.
func Firmware(b *board.Board) (map[string]string, error) {
	if len(b.Firmware) == 0 {
		return nil, nil
	}

	root, cacheDir, err := openCacheRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	fwDir := filepath.Join(b.Name, "firmware")
	if err := root.MkdirAll(fwDir, 0750); err != nil {
		return nil, fmt.Errorf("create firmware dir: %w", err)
	}

	result := make(map[string]string)
	for _, fw := range b.Firmware {
		relPath := filepath.Join(fwDir, fw.Dest)
		if err := download(root, fw.URL, relPath, fw.SHA256); err != nil {
			return nil, fmt.Errorf("download firmware %s: %w", fw.Dest, err)
		}
		result[fw.Dest] = filepath.Join(cacheDir, relPath)
	}

	return result, nil
}

// alpineArch maps board architecture strings to Alpine APK repository
// architecture names.
func alpineArch(arch string) string {
	switch arch {
	case "aarch64":
		return "aarch64"
	case "armv7":
		return "armhf"
	case "x86_64":
		return "x86_64"
	default:
		return arch
	}
}

// checksumFile verifies that the file at relPath within root has the expected
// SHA256 hex digest.
func checksumFile(root *os.Root, relPath, want string) (bool, error) {
	f, err := root.Open(relPath)
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
