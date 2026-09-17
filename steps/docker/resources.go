package docker

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"

	"github.com/up2jj/wuko/process"
)

const (
	nanoCPUsPerCPU     = int64(1_000_000_000)
	minimumMemoryLimit = int64(6 << 20)
)

// ResourceConfig limits the aggregate resources available to one Docker
// executor session. Scalar fields use any so validation can accept both YAML
// numbers and strings produced by template rendering.
type ResourceConfig struct {
	CPUs   any `yaml:"cpus,omitempty"`
	Memory any `yaml:"memory,omitempty"`
	PIDs   any `yaml:"pids,omitempty"`
}

func executorResources(config *ResourceConfig, allowTemplates bool) (container.Resources, error) {
	var resources container.Resources
	if config == nil {
		return resources, nil
	}
	// Templated fields are skipped during pre-render validation. The rendered
	// configuration is decoded and validated again before container creation.
	if config.CPUs != nil && !(allowTemplates && resourceTemplated(config.CPUs)) {
		value, err := parseNanoCPUs(config.CPUs)
		if err != nil {
			return container.Resources{}, fmt.Errorf("cpus %w", err)
		}
		resources.NanoCPUs = value
	}
	if config.Memory != nil && !(allowTemplates && resourceTemplated(config.Memory)) {
		value, err := parseMemory(config.Memory)
		if err != nil {
			return container.Resources{}, fmt.Errorf("memory %w", err)
		}
		resources.Memory = value
	}
	if config.PIDs != nil && !(allowTemplates && resourceTemplated(config.PIDs)) {
		value, err := parsePIDs(config.PIDs)
		if err != nil {
			return container.Resources{}, fmt.Errorf("pids %w", err)
		}
		resources.PidsLimit = &value
	}
	return resources, nil
}

func resourceTemplated(value any) bool {
	text, ok := value.(string)
	return ok && templated(text)
}

func parseNanoCPUs(value any) (int64, error) {
	text, err := decimalResourceValue(value)
	if err != nil {
		return 0, fmt.Errorf("%w; must be a positive decimal with at most 9 fractional digits", err)
	}
	whole, fraction, hasFraction := strings.Cut(text, ".")
	if whole == "" || !decimalDigits(whole) || hasFraction && (fraction == "" || !decimalDigits(fraction) || len(fraction) > 9) {
		return 0, fmt.Errorf("must be a positive decimal with at most 9 fractional digits")
	}
	wholeCPUs, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || wholeCPUs > math.MaxInt64/nanoCPUsPerCPU {
		return 0, fmt.Errorf("exceeds Docker's CPU limit range")
	}
	nanoCPUs := wholeCPUs * nanoCPUsPerCPU
	if hasFraction {
		fraction += strings.Repeat("0", 9-len(fraction))
		fractionalNanoCPUs, parseErr := strconv.ParseInt(fraction, 10, 64)
		if parseErr != nil || nanoCPUs > math.MaxInt64-fractionalNanoCPUs {
			return 0, fmt.Errorf("exceeds Docker's CPU limit range")
		}
		nanoCPUs += fractionalNanoCPUs
	}
	if nanoCPUs <= 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return nanoCPUs, nil
}

func decimalResourceValue(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case int:
		return strconv.FormatInt(int64(value), 10), nil
	case int8:
		return strconv.FormatInt(int64(value), 10), nil
	case int16:
		return strconv.FormatInt(int64(value), 10), nil
	case int32:
		return strconv.FormatInt(int64(value), 10), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case uint:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint64:
		return strconv.FormatUint(value, 10), nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", fmt.Errorf("must be finite")
		}
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("has type %T", value)
	}
}

func decimalDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func parseMemory(value any) (int64, error) {
	text, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("has type %T; must be a positive integer followed by B, KiB, MiB, GiB, or TiB", value)
	}
	bytes, err := process.ParseCaptureLimit(text)
	if err != nil {
		return 0, err
	}
	if bytes < minimumMemoryLimit {
		return 0, fmt.Errorf("must be at least 6MiB")
	}
	return bytes, nil
}

func parsePIDs(value any) (int64, error) {
	var text string
	switch value := value.(type) {
	case string:
		text = value
	case int:
		text = strconv.FormatInt(int64(value), 10)
	case int8:
		text = strconv.FormatInt(int64(value), 10)
	case int16:
		text = strconv.FormatInt(int64(value), 10)
	case int32:
		text = strconv.FormatInt(int64(value), 10)
	case int64:
		text = strconv.FormatInt(value, 10)
	case uint:
		text = strconv.FormatUint(uint64(value), 10)
	case uint8:
		text = strconv.FormatUint(uint64(value), 10)
	case uint16:
		text = strconv.FormatUint(uint64(value), 10)
	case uint32:
		text = strconv.FormatUint(uint64(value), 10)
	case uint64:
		text = strconv.FormatUint(value, 10)
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value >= float64(math.MaxInt64) {
			return 0, fmt.Errorf("must be a positive integer")
		}
		text = strconv.FormatFloat(value, 'f', 0, 64)
	default:
		return 0, fmt.Errorf("has type %T; must be a positive integer", value)
	}
	if text == "" || !decimalDigits(text) {
		return 0, fmt.Errorf("must be a positive integer")
	}
	pids, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("exceeds Docker's PID limit range")
	}
	if pids <= 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return pids, nil
}
