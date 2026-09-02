package cgroup

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseCPULimit parses a CPU limit string and returns cgroup v2 cpu.max format.
func ParseCPULimit(cpuLimit string) (string, error) {
	if cpuLimit == "" || cpuLimit == "max" {
		return "max 100000", nil
	}

	cpu, err := strconv.ParseFloat(cpuLimit, 64)
	if err != nil {
		return "", fmt.Errorf("invalid CPU limit format: %v", err)
	}

	if cpu <= 0 {
		return "", fmt.Errorf("CPU limit must be positive")
	}

	period := 100000
	quota := int64(float64(period) * cpu)

	return fmt.Sprintf("%d %d", quota, period), nil
}

// ParseMemoryLimit parses a memory limit string and returns bytes as a string.
func ParseMemoryLimit(memoryLimit string) (string, error) {
	if memoryLimit == "" || memoryLimit == "max" {
		return "max", nil
	}

	memoryLimit = strings.TrimSpace(memoryLimit)
	memoryLimit = strings.ToUpper(memoryLimit)

	var multiplier int64 = 1
	if strings.HasSuffix(memoryLimit, "K") {
		multiplier = 1024
		memoryLimit = strings.TrimSuffix(memoryLimit, "K")
	} else if strings.HasSuffix(memoryLimit, "M") {
		multiplier = 1024 * 1024
		memoryLimit = strings.TrimSuffix(memoryLimit, "M")
	} else if strings.HasSuffix(memoryLimit, "G") {
		multiplier = 1024 * 1024 * 1024
		memoryLimit = strings.TrimSuffix(memoryLimit, "G")
	}

	value, err := strconv.ParseInt(memoryLimit, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid memory limit format: %v", err)
	}

	if value <= 0 {
		return "", fmt.Errorf("memory limit must be positive")
	}

	bytes := value * multiplier
	return strconv.FormatInt(bytes, 10), nil
}
