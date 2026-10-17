package localdisk

import "testing"

func TestNetworkFilesystems(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		magic   int64
		network bool
	}{
		{
			name:    "NFS",
			magic:   0x6969,
			network: true,
		},
		{
			name:    "9P",
			magic:   0x01021997,
			network: true,
		},
		{
			name:    "CephFS",
			magic:   0x00c36400,
			network: true,
		},
		{
			name:    "Lustre",
			magic:   0x0bd00bd0,
			network: true,
		},
		{
			name:    "virtiofs reports FUSE",
			magic:   0x65735546,
			network: true,
		},
		{
			name:    "ext4",
			magic:   0xef53,
			network: false,
		},
		{
			name:    "XFS",
			magic:   0x58465342,
			network: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, got := networkFilesystems[tc.magic]; got != tc.network {
				t.Errorf("filesystem 0x%x is network = %v, want %v", tc.magic, got, tc.network)
			}
		})
	}
}
