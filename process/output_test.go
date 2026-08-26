package process

import "testing"

func TestParseOutputPolicy(t *testing.T) {
	tests := []struct {
		value string
		want  OutputPolicy
	}{
		{value: "", want: OutputTee},
		{value: "tee", want: OutputTee},
		{value: "inherit", want: OutputInherit},
		{value: "capture", want: OutputCapture},
		{value: "discard", want: OutputDiscard},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := ParseOutputPolicy(test.value)
			if err != nil || got != test.want {
				t.Fatalf("ParseOutputPolicy(%q) = %v, %v; want %v", test.value, got, err, test.want)
			}
		})
	}
	if _, err := ParseOutputPolicy("quiet"); err == nil {
		t.Fatal("ParseOutputPolicy() accepted an unknown policy")
	}
}

func TestParseCaptureLimit(t *testing.T) {
	tests := []struct {
		value   string
		want    int64
		wantErr bool
	}{
		{value: "", want: 0},
		{value: "1B", want: 1},
		{value: "2KiB", want: 2 << 10},
		{value: "3MiB", want: 3 << 20},
		{value: "0B", wantErr: true},
		{value: "-1B", wantErr: true},
		{value: "1MB", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := ParseCaptureLimit(test.value)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("ParseCaptureLimit(%q) = %d, %v; want %d, error=%v", test.value, got, err, test.want, test.wantErr)
			}
		})
	}
}
