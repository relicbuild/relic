package app

import (
	"os"
	"strconv"
	"strings"
)

const cgroupMemoryMax = "/sys/fs/cgroup/memory.max"

const (
	defaultScanMemoryBudget = 1 << 30
	minimumMemoryLimit      = 128 << 20
)

func containerMemory(path string) int64 {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the cgroup file, or a test's
	if err != nil {
		return 0
	}

	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}

	return n
}

func scanMemoryBudget(configured, container int64) int64 {
	switch {
	case configured > 0:
		return configured
	case container > 0:
		return min(container/2, defaultScanMemoryBudget)
	default:
		return defaultScanMemoryBudget
	}
}

func memoryLimit(getenv func(string) string, container, scanBudget int64) int64 {
	if getenv("GOMEMLIMIT") != "" || container <= 0 {
		return 0
	}

	floor := min(minimumMemoryLimit, container/10*9)

	return max((container-scanBudget)/10*9, floor)
}
