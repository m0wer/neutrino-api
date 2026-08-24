package main

import "testing"

func TestConfiguredPrefetchFilters(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset", value: "", want: true},
		{name: "explicit enable", value: "true", want: true},
		{name: "explicit opt-out", value: "false", want: false},
		{name: "invalid uses default", value: "invalid", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PREFETCH_FILTERS", test.value)
			if got := configuredPrefetchFilters(); got != test.want {
				t.Fatalf("configuredPrefetchFilters() = %t, want %t", got, test.want)
			}
		})
	}
}
