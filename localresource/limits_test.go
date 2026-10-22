package localresource

import "testing"

func TestParseResourceLimits(t *testing.T) {
	for _, test := range []struct {
		name, cpus, memory string
		want               Limits
		invalid            bool
	}{
		{name: "unset"},
		{name: "combined", cpus: "1.5", memory: "512MiB", want: Limits{CPUs: 1.5, Memory: 512 << 20}},
		{name: "CPU only", cpus: "0.01", want: Limits{CPUs: .01}},
		{name: "memory only", memory: "1GiB", want: Limits{Memory: 1 << 30}},
		{name: "zero CPU", cpus: "0", invalid: true},
		{name: "negative CPU", cpus: "-1", invalid: true},
		{name: "NaN", cpus: "NaN", invalid: true},
		{name: "infinite", cpus: "+Inf", invalid: true},
		{name: "too small", cpus: "0.001", invalid: true},
		{name: "overflow", cpus: "1e100", invalid: true},
		{name: "bad memory unit", memory: "512MB", invalid: true},
		{name: "memory zero", memory: "0B", invalid: true},
		{name: "memory overflow", memory: "9000000TiB", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.cpus, test.memory)
			if (err != nil) != test.invalid {
				t.Fatalf("Parse() error = %v", err)
			}
			if err == nil && got != test.want {
				t.Fatalf("Parse() = %#v, want %#v", got, test.want)
			}
		})
	}
}
