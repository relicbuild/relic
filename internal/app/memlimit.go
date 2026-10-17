package app

import (
	"os"
	"strconv"
	"strings"
)

const cgroupMemoryMax = "/sys/fs/cgroup/memory.max"

func memoryLimit(getenv func(string) string, path string) int64 {
	if getenv("GOMEMLIMIT") != "" {
		return 0
	}

	b, err := os.ReadFile(path) //nolint:gosec // G304: the cgroup file, or a test's
	if err != nil {
		return 0
	}

	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}

	return n / 10 * 9
}
