package localresource

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScopeArgumentsDisableExpansionAndShareOneBudget(t *testing.T) {
	args := scopeArgs(Limits{CPUs: 1.5, Memory: 512 << 20}, "wuko-test.scope", "/usr/bin/wuko", []string{"run", "--var=literal=$HOME", "test"})
	for _, want := range []string{"--user", "--scope", "--expand-environment=no", "--property=CPUQuota=150%", "--property=MemoryMax=536870912", "--property=MemorySwapMax=0", "--property=OOMPolicy=kill"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %#v", want, args)
		}
	}
	want := []string{"--", "/usr/bin/wuko", workerArgument, "1.5", "536870912B", "run", "--var=literal=$HOME", "test"}
	if !slices.Equal(args[len(args)-len(want):], want) {
		t.Fatalf("worker arguments = %#v", args)
	}
}

func TestVerifyResourceFilesRejectsUnenforcedBudgets(t *testing.T) {
	for _, test := range []struct {
		name, cpu, memory, swap, oom string
		invalid                      bool
	}{
		{name: "enforced", cpu: "150000 100000", memory: "536870912", swap: "0", oom: "1"},
		{name: "tighter", cpu: "100000 100000", memory: "268435456", swap: "0", oom: "1"},
		{name: "unlimited CPU", cpu: "max 100000", memory: "536870912", swap: "0", oom: "1", invalid: true},
		{name: "excess CPU", cpu: "200000 100000", memory: "536870912", swap: "0", oom: "1", invalid: true},
		{name: "unlimited memory", cpu: "150000 100000", memory: "max", swap: "0", oom: "1", invalid: true},
		{name: "excess memory", cpu: "150000 100000", memory: "1073741824", swap: "0", oom: "1", invalid: true},
		{name: "swap allowed", cpu: "150000 100000", memory: "536870912", swap: "536870912", oom: "1", invalid: true},
		{name: "partial OOM", cpu: "150000 100000", memory: "536870912", swap: "0", oom: "0", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{"cpu.max": test.cpu, "memory.max": test.memory, "memory.swap.max": test.swap, "memory.oom.group": test.oom}
			read := func(path string) ([]byte, error) { return []byte(files[filepath.Base(path)]), nil }
			err := verifyFiles("/scope", Limits{CPUs: 1.5, Memory: 512 << 20}, read)
			if (err != nil) != test.invalid {
				t.Fatalf("verifyFiles() = %v", err)
			}
		})
	}
	if err := verifyFiles("/scope", Limits{Memory: 100}, func(string) ([]byte, error) { return nil, os.ErrPermission }); err == nil {
		t.Fatal("accepted unreadable controller")
	}
}

func TestUnifiedCgroupRejectsRootAndEscapingPaths(t *testing.T) {
	for _, data := range []string{"0::/\n", "0::/../outside\n", "0::/a/../../b\n", "2:cpu:/scope\n"} {
		if _, err := unifiedCgroup(data); err == nil {
			t.Errorf("accepted %q", data)
		}
	}
	got, err := unifiedCgroup("0::/user.slice/wuko.scope\n")
	if err != nil || !strings.HasSuffix(got, "/user.slice/wuko.scope") {
		t.Fatalf("unifiedCgroup() = %q, %v", got, err)
	}
}
