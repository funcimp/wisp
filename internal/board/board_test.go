package board

import (
	"testing"
)

func TestIsQEMU(t *testing.T) {
	tests := []struct {
		name string
		qemu *QEMUConfig
		want bool
	}{
		{
			name: "nil QEMU config",
			qemu: nil,
			want: false,
		},
		{
			name: "with QEMU config",
			qemu: &QEMUConfig{Binary: "qemu-system-aarch64", Machine: "virt", Memory: "512M"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Board{QEMU: tt.qemu}
			if got := b.IsQEMU(); got != tt.want {
				t.Errorf("IsQEMU() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, b *Board)
	}{
		{
			name:    "missing name",
			input:   `{"arch": "aarch64"}`,
			wantErr: true,
		},
		{
			name:    "missing arch",
			input:   `{"name": "test"}`,
			wantErr: true,
		},
		{
			name:  "minimal board without QEMU",
			input: `{"name": "test", "arch": "aarch64"}`,
			check: func(t *testing.T, b *Board) {
				if b.QEMU != nil {
					t.Error("expected QEMU to be nil")
				}
				if b.IsQEMU() {
					t.Error("expected IsQEMU() to be false")
				}
			},
		},
		{
			name: "board with QEMU config",
			input: `{
				"name": "qemu",
				"arch": "aarch64",
				"qemu": {
					"binary": "qemu-system-aarch64",
					"machine": "virt",
					"cpu": "host",
					"memory": "512M",
					"accel": "hvf",
					"net_dev": "virtio-net-pci"
				}
			}`,
			check: func(t *testing.T, b *Board) {
				if b.QEMU == nil {
					t.Fatal("expected QEMU to be non-nil")
				}
				if !b.IsQEMU() {
					t.Error("expected IsQEMU() to be true")
				}
				if b.QEMU.Binary != "qemu-system-aarch64" {
					t.Errorf("Binary = %q, want %q", b.QEMU.Binary, "qemu-system-aarch64")
				}
				if b.QEMU.Machine != "virt" {
					t.Errorf("Machine = %q, want %q", b.QEMU.Machine, "virt")
				}
				if b.QEMU.CPU != "host" {
					t.Errorf("CPU = %q, want %q", b.QEMU.CPU, "host")
				}
				if b.QEMU.Memory != "512M" {
					t.Errorf("Memory = %q, want %q", b.QEMU.Memory, "512M")
				}
				if b.QEMU.Accel != "hvf" {
					t.Errorf("Accel = %q, want %q", b.QEMU.Accel, "hvf")
				}
				if b.QEMU.NetDev != "virtio-net-pci" {
					t.Errorf("NetDev = %q, want %q", b.QEMU.NetDev, "virtio-net-pci")
				}
			},
		},
		{
			name: "board with QEMU extra args",
			input: `{
				"name": "raspi3b",
				"arch": "aarch64",
				"qemu": {
					"binary": "qemu-system-aarch64",
					"machine": "raspi3b",
					"cpu": "cortex-a53",
					"memory": "1G",
					"accel": "tcg",
					"net_dev": "usb-net",
					"extra": ["-usb", "-device", "usb-hub,bus=usb-bus.0"]
				}
			}`,
			check: func(t *testing.T, b *Board) {
				if b.QEMU == nil {
					t.Fatal("expected QEMU to be non-nil")
				}
				if len(b.QEMU.Extra) != 3 {
					t.Fatalf("Extra length = %d, want 3", len(b.QEMU.Extra))
				}
				wantExtra := []string{"-usb", "-device", "usb-hub,bus=usb-bus.0"}
				for i, want := range wantExtra {
					if b.QEMU.Extra[i] != want {
						t.Errorf("Extra[%d] = %q, want %q", i, b.QEMU.Extra[i], want)
					}
				}
			},
		},
		{
			name:    "invalid JSON",
			input:   `{not json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := Parse([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, b)
			}
		})
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantErr bool
		check   func(t *testing.T, b *Board)
	}{
		{
			name:   "qemu target",
			target: "qemu",
			check: func(t *testing.T, b *Board) {
				if b.Name != "qemu" {
					t.Errorf("Name = %q, want %q", b.Name, "qemu")
				}
				if !b.IsQEMU() {
					t.Error("expected IsQEMU() to be true")
				}
				if b.QEMU.Machine != "virt" {
					t.Errorf("Machine = %q, want %q", b.QEMU.Machine, "virt")
				}
			},
		},
		{
			name:   "raspi3b target",
			target: "raspi3b",
			check: func(t *testing.T, b *Board) {
				if b.Name != "raspi3b" {
					t.Errorf("Name = %q, want %q", b.Name, "raspi3b")
				}
				if !b.IsQEMU() {
					t.Error("expected IsQEMU() to be true")
				}
				if b.QEMU.Machine != "raspi3b" {
					t.Errorf("Machine = %q, want %q", b.QEMU.Machine, "raspi3b")
				}
			},
		},
		{
			name:   "pi5 target",
			target: "pi5",
			check: func(t *testing.T, b *Board) {
				if b.Name != "pi5" {
					t.Errorf("Name = %q, want %q", b.Name, "pi5")
				}
				if b.IsQEMU() {
					t.Error("expected IsQEMU() to be false")
				}
			},
		},
		{
			name:    "unknown target",
			target:  "nonexistent",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := Get(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, b)
			}
		})
	}
}

func TestList(t *testing.T) {
	boards, err := List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(boards) < 3 {
		t.Fatalf("expected at least 3 boards, got %d", len(boards))
	}

	names := make(map[string]bool)
	for _, b := range boards {
		names[b.Name] = true
	}

	for _, want := range []string{"pi5", "qemu", "raspi3b"} {
		if !names[want] {
			t.Errorf("expected board %q in list", want)
		}
	}
}
