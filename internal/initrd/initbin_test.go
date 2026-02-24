package initrd

import "testing"

func TestGoarch(t *testing.T) {
	tests := []struct {
		boardArch string
		want      string
		wantErr   bool
	}{
		{boardArch: "aarch64", want: "arm64"},
		{boardArch: "riscv64", want: "riscv64"},
		{boardArch: "x86_64", want: "amd64"},
		{boardArch: "mips64", wantErr: true},
		{boardArch: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.boardArch, func(t *testing.T) {
			got, err := goarch(tt.boardArch)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("goarch(%q) = %q, want error", tt.boardArch, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("goarch(%q) error: %v", tt.boardArch, err)
			}
			if got != tt.want {
				t.Errorf("goarch(%q) = %q, want %q", tt.boardArch, got, tt.want)
			}
		})
	}
}
