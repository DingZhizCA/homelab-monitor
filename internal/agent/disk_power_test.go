package agent

import "testing"

func TestParseATAPowerState(t *testing.T) {
	tests := map[string]string{
		"drive state is:  active/idle": "active",
		"drive state is:  standby":     "standby",
		"drive state is:  sleeping":    "sleeping",
		"drive state is:  unknown":     "unknown",
		"permission denied":            "unknown",
	}
	for input, want := range tests {
		if got := parseATAPowerState(input); got != want {
			t.Errorf("parseATAPowerState(%q) = %q, want %q", input, got, want)
		}
	}
}
