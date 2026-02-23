// Package initrd creates gzipped cpio archives in the "newc" (SVR4) format
// used by Linux for initramfs images. It uses only the Go standard library.
package initrd

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
)

// Entry represents a file or directory in the initrd archive.
type Entry struct {
	Path string      // path without leading slash, e.g. "init" or "etc/wisp.conf"
	Data []byte      // file contents; nil for directories
	Mode os.FileMode // permissions, e.g. 0755 for executables, os.ModeDir|0755 for dirs
}

// Write creates a gzipped cpio (newc format) archive from entries and writes
// it to w. Parent directories are inferred from file paths and created
// automatically. Empty directories (e.g., mount points like "dev", "proc")
// must be listed explicitly as entries with nil Data and os.ModeDir set.
func Write(w io.Writer, entries []Entry) error {
	gz := gzip.NewWriter(w)

	// Separate explicit directories from files. Collect all directories
	// needed (both explicit and inferred from file paths).
	dirSet := make(map[string]bool)
	var files []Entry

	for _, e := range entries {
		if e.Mode.IsDir() {
			dirSet[e.Path] = true
			continue
		}
		files = append(files, e)
		// Infer parent directories.
		for dir := path.Dir(e.Path); dir != "." && dir != ""; dir = path.Dir(dir) {
			dirSet[dir] = true
		}
	}

	// Sort directories so parents come before children.
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	// Write all entries: directories first, then files.
	ino := uint32(1)

	for _, d := range dirs {
		if err := writeCpioEntry(gz, d, 0040755, nil, ino); err != nil {
			return fmt.Errorf("dir %s: %w", d, err)
		}
		ino++
	}

	for _, f := range files {
		mode := cpioMode(f.Mode)
		if err := writeCpioEntry(gz, f.Path, mode, f.Data, ino); err != nil {
			return fmt.Errorf("file %s: %w", f.Path, err)
		}
		ino++
	}

	// Trailer marks end of archive.
	if err := writeCpioEntry(gz, "TRAILER!!!", 0, nil, 0); err != nil {
		return fmt.Errorf("trailer: %w", err)
	}

	return gz.Close()
}

// writeCpioEntry writes a single cpio newc entry (header + name + data).
func writeCpioEntry(w io.Writer, name string, mode uint32, data []byte, ino uint32) error {
	nameBytes := name + "\x00" // null-terminated
	nameSize := len(nameBytes)
	fileSize := len(data)

	// Directories get nlink=2, files get nlink=1.
	nlink := uint32(1)
	if mode&0040000 != 0 {
		nlink = 2
	}

	// Header: 110 bytes of ASCII hex fields.
	hdr := fmt.Sprintf("070701"+
		"%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		ino,      // ino
		mode,     // mode
		0,        // uid
		0,        // gid
		nlink,    // nlink
		0,        // mtime
		fileSize, // filesize
		0, 0,     // devmajor, devminor
		0, 0,     // rdevmajor, rdevminor
		nameSize, // namesize
		0,        // checksum
	)

	if _, err := io.WriteString(w, hdr); err != nil {
		return err
	}

	// Write filename.
	if _, err := io.WriteString(w, nameBytes); err != nil {
		return err
	}

	// Pad header+name to 4-byte boundary.
	if err := writePad(w, 110+nameSize); err != nil {
		return err
	}

	// Write file data.
	if fileSize > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
		// Pad data to 4-byte boundary.
		if err := writePad(w, fileSize); err != nil {
			return err
		}
	}

	return nil
}

// writePad writes zero bytes to pad n up to the next 4-byte boundary.
func writePad(w io.Writer, n int) error {
	pad := align4(n) - n
	if pad > 0 {
		_, err := w.Write(make([]byte, pad))
		return err
	}
	return nil
}

// cpioMode converts a Go os.FileMode to the cpio/stat mode format.
func cpioMode(m os.FileMode) uint32 {
	perm := uint32(m.Perm())
	if m.IsDir() {
		return 0040000 | perm
	}
	return 0100000 | perm
}

// align4 rounds n up to the nearest multiple of 4.
func align4(n int) int {
	return (n + 3) &^ 3
}
