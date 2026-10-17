package localdisk

import (
	"fmt"

	"golang.org/x/sys/unix"
)

var networkFilesystems = map[int64]string{
	0x6969:     "NFS",
	0x517b:     "SMB",
	0xff534d42: "CIFS",
	0xfe534d42: "SMB2",
	0x65735546: "FUSE",
	0x564c:     "NCP",
	0x73757245: "Coda",
	0x5346414f: "AFS",
	0x6b414653: "AFS",
	0x01021997: "9P",
	0x00c36400: "CephFS",
	0x0bd00bd0: "Lustre",
}

// Check refuses a directory on a network filesystem.
func Check(dir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", dir, err)
	}

	if name, ok := networkFilesystems[int64(st.Type)]; ok { //nolint:unconvert // Type is int32 on some architectures
		return fmt.Errorf("%s is on a network filesystem (%s); Relic needs local disk for it", dir, name)
	}

	return nil
}
