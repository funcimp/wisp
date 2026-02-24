package initrd

import (
	"os"
	"testing"
)

func TestBuildEntries(t *testing.T) {
	tests := []struct {
		name     string
		initData []byte
		svcData  []byte
		net      NetworkConfig
		modules  []moduleFile
		want     map[string]struct {
			isDir bool
			data  string
			mode  os.FileMode
		}
	}{
		{
			name:     "basic without modules",
			initData: []byte("INIT"),
			svcData:  []byte("SERVICE"),
			net: NetworkConfig{
				Interface: "eth0",
				Address:   "10.0.2.15/24",
				Gateway:   "10.0.2.2",
				DNS:       "10.0.2.3",
			},
			modules: nil,
			want: map[string]struct {
				isDir bool
				data  string
				mode  os.FileMode
			}{
				"dev":              {isDir: true, mode: os.ModeDir | 0755},
				"proc":             {isDir: true, mode: os.ModeDir | 0755},
				"sys":              {isDir: true, mode: os.ModeDir | 0755},
				"init":             {data: "INIT", mode: 0755},
				"service/run":      {data: "SERVICE", mode: 0755},
				"etc/wisp.conf":    {data: "IFACE=eth0\nADDR=10.0.2.15/24\nGW=10.0.2.2\n", mode: 0644},
				"etc/resolv.conf":  {data: "nameserver 10.0.2.3\n", mode: 0644},
			},
		},
		{
			name:     "with kernel modules",
			initData: []byte("INIT"),
			svcData:  []byte("SERVICE"),
			net: NetworkConfig{
				Interface: "eth0",
				Address:   "192.168.1.100/24",
				Gateway:   "192.168.1.1",
				DNS:       "192.168.1.1",
			},
			modules: []moduleFile{
				{name: "failover.ko", data: []byte("MOD1")},
				{name: "virtio_net.ko", data: []byte("MOD2")},
			},
			want: map[string]struct {
				isDir bool
				data  string
				mode  os.FileMode
			}{
				"dev":                       {isDir: true, mode: os.ModeDir | 0755},
				"proc":                      {isDir: true, mode: os.ModeDir | 0755},
				"sys":                       {isDir: true, mode: os.ModeDir | 0755},
				"init":                      {data: "INIT", mode: 0755},
				"service/run":               {data: "SERVICE", mode: 0755},
				"etc/wisp.conf":             {data: "IFACE=eth0\nADDR=192.168.1.100/24\nGW=192.168.1.1\n", mode: 0644},
				"etc/resolv.conf":           {data: "nameserver 192.168.1.1\n", mode: 0644},
				"lib/modules/failover.ko":   {data: "MOD1", mode: 0644},
				"lib/modules/virtio_net.ko": {data: "MOD2", mode: 0644},
				"etc/modules":               {data: "failover.ko\nvirtio_net.ko\n", mode: 0644},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := buildEntries(tt.initData, tt.svcData, tt.net, tt.modules)

			got := make(map[string]Entry)
			for _, e := range entries {
				got[e.Path] = e
			}

			for path, want := range tt.want {
				e, ok := got[path]
				if !ok {
					t.Errorf("missing entry: %s", path)
					continue
				}
				if want.isDir {
					if e.Mode != want.mode {
						t.Errorf("%s: mode = %v, want %v", path, e.Mode, want.mode)
					}
					if e.Data != nil {
						t.Errorf("%s: dir has non-nil data", path)
					}
				} else {
					if string(e.Data) != want.data {
						t.Errorf("%s: data = %q, want %q", path, e.Data, want.data)
					}
					if e.Mode != want.mode {
						t.Errorf("%s: mode = %v, want %v", path, e.Mode, want.mode)
					}
				}
				delete(got, path)
			}

			for path := range got {
				t.Errorf("unexpected entry: %s", path)
			}
		})
	}
}
