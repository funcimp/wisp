package image

import (
	"testing"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name      string
		files     []File
		wantFiles []string // expected root directory entries
		wantErr   bool
	}{
		{
			name: "single file",
			files: []File{
				{Name: "config.txt", Data: []byte("arm_64bit=1\n")},
			},
			wantFiles: []string{"CONFIG.TXT"},
		},
		{
			name: "multiple files",
			files: []File{
				{Name: "config.txt", Data: []byte("arm_64bit=1\n")},
				{Name: "cmdline.txt", Data: []byte("rdinit=/init\n")},
				{Name: "Image", Data: []byte("kernel-data")},
				{Name: "initrd.img", Data: []byte("cpio-data")},
			},
			wantFiles: []string{"CONFIG.TXT", "CMDLINE.TXT", "IMAGE", "INITRD.IMG"},
		},
		{
			name: "with subdirectory",
			files: []File{
				{Name: "config.txt", Data: []byte("test")},
				{Name: "overlays/miniuart.dtbo", Data: []byte("overlay-data")},
			},
			wantFiles: []string{"CONFIG.TXT", "OVERLAYS/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imgData, err := Build(tt.files)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			if len(imgData) != defaultImageSize {
				t.Fatalf("image size: got %d, want %d", len(imgData), defaultImageSize)
			}

			// Verify MBR signature.
			if imgData[510] != 0x55 || imgData[511] != 0xAA {
				t.Fatal("missing MBR boot signature")
			}

			// List root directory files.
			names, err := ListFiles(imgData)
			if err != nil {
				t.Fatalf("list files: %v", err)
			}

			for _, want := range tt.wantFiles {
				found := false
				for _, got := range names {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("file %q not found in root directory (got: %v)", want, names)
				}
			}
		})
	}
}

func TestBuildFileContent(t *testing.T) {
	content := []byte("arm_64bit=1\nkernel=Image\ninitramfs initrd.img followkernel\n")
	files := []File{
		{Name: "config.txt", Data: content},
	}

	imgData, err := Build(files)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := ReadFile(imgData, "config.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != string(content) {
		t.Fatalf("content mismatch:\ngot:  %q\nwant: %q", got, content)
	}
}

func TestToShortName(t *testing.T) {
	tests := []struct {
		input string
		want  string // 11-char representation
	}{
		{"config.txt", "CONFIG  TXT"},
		{"Image", "IMAGE      "},
		{"initrd.img", "INITRD  IMG"},
		{"start4.elf", "START4  ELF"},
		{"fixup4.dat", "FIXUP4  DAT"},
		{"overlays", "OVERLAYS   "},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			sn := toShortName(tt.input)
			got := string(sn[:])
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBPBSignature(t *testing.T) {
	files := []File{
		{Name: "test.txt", Data: []byte("test")},
	}

	imgData, err := Build(files)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Check partition BPB signature.
	partStart := 2048 * sectorSize
	bpb := imgData[partStart:]
	if bpb[510] != 0x55 || bpb[511] != 0xAA {
		t.Fatal("missing BPB boot signature")
	}

	// Check OEM name.
	oem := string(bpb[3:11])
	if oem != "WISP    " {
		t.Fatalf("OEM name: got %q, want %q", oem, "WISP    ")
	}

	// Check filesystem type.
	fsType := string(bpb[82:90])
	if fsType != "FAT32   " {
		t.Fatalf("FS type: got %q, want %q", fsType, "FAT32   ")
	}
}
