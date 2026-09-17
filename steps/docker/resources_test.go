package docker

import (
	"io"
	"math"
	"strings"
	"testing"

	"github.com/up2jj/wuko/executor"
)

func TestDockerExecutorAppliesContainerResources(t *testing.T) {
	tests := []struct {
		name      string
		resources map[string]any
	}{
		{
			name: "YAML numeric scalars",
			resources: map[string]any{
				"cpus": 1.5, "memory": "512MiB", "pids": 128,
			},
		},
		{
			name: "rendered strings",
			resources: map[string]any{
				"cpus": "1.5", "memory": "512MiB", "pids": "128",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{}
			providerValue, err := NewExecutor(map[string]any{
				"image": "alpine", "resources": test.resources,
			})
			if err != nil {
				t.Fatal(err)
			}
			provider := providerValue.(*ExecutorProvider)
			provider.newClient = func() (dockerClient, error) { return client, nil }
			session, err := provider.Open(t.Context(), executor.Request{
				WorkflowName: "limited", RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := session.Close(t.Context()); closeErr != nil {
					t.Errorf("Close() error = %v", closeErr)
				}
			})

			resources := client.created.HostConfig.Resources
			if resources.NanoCPUs != 1_500_000_000 || resources.Memory != 512<<20 {
				t.Fatalf("resources = %#v", resources)
			}
			if resources.PidsLimit == nil || *resources.PidsLimit != 128 {
				t.Fatalf("PidsLimit = %v, want 128", resources.PidsLimit)
			}
			if resources.MemorySwap != 0 {
				t.Fatalf("MemorySwap = %d, want Docker default 0", resources.MemorySwap)
			}
		})
	}
}

func TestDockerExecutorLeavesResourcesUnsetByDefault(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  map[string]any
	}{
		{name: "omitted", raw: map[string]any{"image": "alpine"}},
		{name: "empty", raw: map[string]any{"image": "alpine", "resources": map[string]any{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{}
			providerValue, err := NewExecutor(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			provider := providerValue.(*ExecutorProvider)
			provider.newClient = func() (dockerClient, error) { return client, nil }
			session, err := provider.Open(t.Context(), executor.Request{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close(t.Context()) })

			resources := client.created.HostConfig.Resources
			if resources.NanoCPUs != 0 || resources.Memory != 0 || resources.MemorySwap != 0 || resources.PidsLimit != nil {
				t.Fatalf("resources = %#v, want zero values", resources)
			}
		})
	}
}

func TestDockerExecutorResourceValidation(t *testing.T) {
	tests := []struct {
		name      string
		resources map[string]any
		want      string
	}{
		{name: "zero CPUs", resources: map[string]any{"cpus": 0}, want: "cpus must be greater than zero"},
		{name: "negative CPUs", resources: map[string]any{"cpus": -1}, want: "cpus must be a positive decimal"},
		{name: "over-precise CPUs", resources: map[string]any{"cpus": "0.0000000001"}, want: "cpus must be a positive decimal"},
		{name: "infinite CPUs", resources: map[string]any{"cpus": math.Inf(1)}, want: "cpus must be finite"},
		{name: "CPU overflow", resources: map[string]any{"cpus": "9223372037"}, want: "cpus exceeds"},
		{name: "memory below minimum", resources: map[string]any{"memory": "5MiB"}, want: "memory must be at least 6MiB"},
		{name: "memory unit", resources: map[string]any{"memory": "64MB"}, want: "memory must be a positive integer"},
		{name: "memory overflow", resources: map[string]any{"memory": "9000000TiB"}, want: "memory must be a positive integer"},
		{name: "numeric memory", resources: map[string]any{"memory": 64}, want: "memory has type"},
		{name: "zero PIDs", resources: map[string]any{"pids": 0}, want: "pids must be greater than zero"},
		{name: "fractional PIDs", resources: map[string]any{"pids": 1.5}, want: "pids must be a positive integer"},
		{name: "PID overflow", resources: map[string]any{"pids": "9223372036854775808"}, want: "pids exceeds"},
		{name: "PID type", resources: map[string]any{"pids": true}, want: "pids has type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewExecutor(map[string]any{"image": "alpine", "resources": test.resources})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewExecutor() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDockerExecutorResourceSchemaAndTemplates(t *testing.T) {
	_, err := NewExecutor(map[string]any{
		"image": "alpine",
		"resources": map[string]any{
			"cpus": "{{ .vars.cpus }}", "memory": "{{ .vars.memory }}", "pids": "{{ .vars.pids }}",
		},
	})
	if err != nil {
		t.Fatalf("NewExecutor() rejected templated resources: %v", err)
	}

	_, err = NewExecutor(map[string]any{
		"image": "alpine", "resources": map[string]any{"shares": 100},
	})
	if err == nil || !strings.Contains(err.Error(), "field shares not found") {
		t.Fatalf("NewExecutor() error = %v, want unknown nested field", err)
	}
}

func TestDockerExecutorRejectsUnrenderedResourcesBeforeCreate(t *testing.T) {
	client := &fakeClient{}
	session := &dockerExecutorSession{
		config: ExecutorConfig{
			Image: "alpine", Pull: "never", Workspace: &WorkspaceConfig{Enabled: false},
			Resources: &ResourceConfig{CPUs: "{{ .vars.cpus }}"},
		},
		client: client,
	}

	err := session.startLocked(t.Context())
	if err == nil || !strings.Contains(err.Error(), "resources: cpus") {
		t.Fatalf("startLocked() error = %v, want unrendered CPU error", err)
	}
	if client.created.Config != nil {
		t.Fatalf("invalid resources created container: %#v", client.created)
	}
}
