package localresource

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func scopeArgs(limits Limits, unit, executable string, args []string) []string {
	result := []string{"--user", "--scope", "--quiet", "--collect", "--expand-environment=no", "--unit=" + unit}
	if limits.CPUs > 0 {
		result = append(result, "--property=CPUQuota="+strconv.FormatFloat(limits.CPUs*100, 'f', -1, 64)+"%", "--property=CPUQuotaPeriodSec=100ms")
	}
	if limits.Memory > 0 {
		result = append(result, "--property=MemoryMax="+strconv.FormatInt(limits.Memory, 10), "--property=MemorySwapMax=0", "--property=OOMPolicy=kill")
	}
	cpus, memory := "", ""
	if limits.CPUs > 0 {
		cpus = strconv.FormatFloat(limits.CPUs, 'f', -1, 64)
	}
	if limits.Memory > 0 {
		memory = strconv.FormatInt(limits.Memory, 10) + "B"
	}
	result = append(result, "--", executable, workerArgument, cpus, memory)
	return append(result, args...)
}

func unifiedCgroup(data string) (string, error) {
	for line := range strings.SplitSeq(data, "\n") {
		if path, found := strings.CutPrefix(line, "0::/"); found {
			if path == "" || filepath.Clean(path) != path || path == ".." || strings.HasPrefix(path, "../") {
				break
			}
			return filepath.Join("/sys/fs/cgroup", path), nil
		}
	}
	return "", fmt.Errorf("cannot locate a non-root cgroup v2 resource scope")
}

func verifyFiles(dir string, limits Limits, read func(string) ([]byte, error)) error {
	if limits.CPUs > 0 {
		data, err := read(filepath.Join(dir, "cpu.max"))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(data))
		if len(fields) != 2 {
			return fmt.Errorf("invalid cpu.max")
		}
		quota, err := strconv.ParseUint(fields[0], 10, 64)
		period, periodErr := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || periodErr != nil || quota == 0 || period == 0 || float64(quota) > limits.CPUs*float64(period)+1 {
			return fmt.Errorf("cpu.max does not enforce the requested CPU limit")
		}
	}
	if limits.Memory > 0 {
		for name, maximum := range map[string]int64{"memory.max": limits.Memory, "memory.swap.max": 0, "memory.oom.group": 1} {
			data, err := read(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			if err != nil || value < 0 || value > maximum || name == "memory.oom.group" && value != 1 {
				return fmt.Errorf("%s does not enforce the requested memory policy", name)
			}
		}
	}
	return nil
}
