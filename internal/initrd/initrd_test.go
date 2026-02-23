package initrd

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"testing"
)

// cpioEntry is a parsed cpio newc entry for test verification.
type cpioEntry struct {
	name     string
	mode     uint32
	filesize int
	data     []byte
}

// parseCpio reads all entries from a cpio newc archive.
func parseCpio(r io.Reader) ([]cpioEntry, error) {
	var entries []cpioEntry
	for {
		// Read 110-byte header.
		hdr := make([]byte, 110)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return entries, fmt.Errorf("read header: %w", err)
		}
		if string(hdr[:6]) != "070701" {
			return entries, fmt.Errorf("bad magic: %q", hdr[:6])
		}

		// Parse hex fields.
		mode := hexVal(hdr[14:22])
		filesize := hexVal(hdr[54:62])
		namesize := hexVal(hdr[94:102])

		// Read filename.
		nameBuf := make([]byte, namesize)
		if _, err := io.ReadFull(r, nameBuf); err != nil {
			return entries, fmt.Errorf("read name: %w", err)
		}
		name := string(nameBuf[:namesize-1]) // strip null terminator

		// Skip header+name padding.
		padAfterName := align4(110+int(namesize)) - (110 + int(namesize))
		if padAfterName > 0 {
			io.ReadFull(r, make([]byte, padAfterName))
		}

		if name == "TRAILER!!!" {
			break
		}

		// Read file data.
		var data []byte
		if filesize > 0 {
			data = make([]byte, filesize)
			if _, err := io.ReadFull(r, data); err != nil {
				return entries, fmt.Errorf("read data for %s: %w", name, err)
			}
			// Skip data padding.
			padAfterData := align4(int(filesize)) - int(filesize)
			if padAfterData > 0 {
				io.ReadFull(r, make([]byte, padAfterData))
			}
		}

		entries = append(entries, cpioEntry{
			name:     name,
			mode:     uint32(mode),
			filesize: int(filesize),
			data:     data,
		})
	}
	return entries, nil
}

func hexVal(b []byte) uint64 {
	dst := make([]byte, 4)
	_, _ = hex.Decode(dst, b)
	return uint64(dst[0])<<24 | uint64(dst[1])<<16 | uint64(dst[2])<<8 | uint64(dst[3])
}

func TestWrite(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		want    map[string]struct {
			isDir bool
			data  string
			mode  uint32
		}
	}{
		{
			name: "basic initrd with files and directories",
			entries: []Entry{
				{Path: "dev", Mode: os.ModeDir | 0755},
				{Path: "proc", Mode: os.ModeDir | 0755},
				{Path: "sys", Mode: os.ModeDir | 0755},
				{Path: "init", Data: []byte("INIT_BINARY"), Mode: 0755},
				{Path: "etc/wisp.conf", Data: []byte("IFACE=eth0\n"), Mode: 0644},
				{Path: "service/run", Data: []byte("SERVICE_BINARY"), Mode: 0755},
			},
			want: map[string]struct {
				isDir bool
				data  string
				mode  uint32
			}{
				"dev":           {isDir: true, mode: 0040755},
				"etc":           {isDir: true, mode: 0040755},   // auto-inferred
				"proc":          {isDir: true, mode: 0040755},
				"service":       {isDir: true, mode: 0040755},   // auto-inferred
				"sys":           {isDir: true, mode: 0040755},
				"init":          {data: "INIT_BINARY", mode: 0100755},
				"etc/wisp.conf": {data: "IFACE=eth0\n", mode: 0100644},
				"service/run":   {data: "SERVICE_BINARY", mode: 0100755},
			},
		},
		{
			name: "nested directories are inferred",
			entries: []Entry{
				{Path: "lib/modules/virtio_net.ko", Data: []byte("MODULE"), Mode: 0644},
			},
			want: map[string]struct {
				isDir bool
				data  string
				mode  uint32
			}{
				"lib":                        {isDir: true, mode: 0040755},
				"lib/modules":                {isDir: true, mode: 0040755},
				"lib/modules/virtio_net.ko":  {data: "MODULE", mode: 0100644},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write archive.
			var buf bytes.Buffer
			if err := Write(&buf, tt.entries); err != nil {
				t.Fatalf("Write: %v", err)
			}

			// Decompress gzip.
			gz, err := gzip.NewReader(&buf)
			if err != nil {
				t.Fatalf("gzip.NewReader: %v", err)
			}
			defer gz.Close()

			// Parse cpio entries.
			got, err := parseCpio(gz)
			if err != nil {
				t.Fatalf("parseCpio: %v", err)
			}

			// Build map of parsed entries.
			gotMap := make(map[string]cpioEntry)
			for _, e := range got {
				gotMap[e.name] = e
			}

			// Verify expected entries exist with correct values.
			for path, want := range tt.want {
				e, ok := gotMap[path]
				if !ok {
					t.Errorf("missing entry: %s", path)
					continue
				}
				if e.mode != want.mode {
					t.Errorf("%s: mode = %06o, want %06o", path, e.mode, want.mode)
				}
				if want.isDir {
					if e.filesize != 0 {
						t.Errorf("%s: dir has filesize %d, want 0", path, e.filesize)
					}
				} else {
					if string(e.data) != want.data {
						t.Errorf("%s: data = %q, want %q", path, e.data, want.data)
					}
				}
				delete(gotMap, path)
			}

			// Check no unexpected entries.
			for path := range gotMap {
				t.Errorf("unexpected entry: %s", path)
			}
		})
	}
}
