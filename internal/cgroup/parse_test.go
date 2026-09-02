package cgroup

import "testing"

func TestParseCPULimit(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"1", "100000 100000", false},
		{"0.5", "50000 100000", false},
		{"2", "200000 100000", false},
		{"max", "max 100000", false},
		{"", "max 100000", false},
		{"-1", "", true},
		{"invalid", "", true},
	}

	for _, test := range tests {
		result, err := ParseCPULimit(test.input)
		if test.hasError {
			if err == nil {
				t.Errorf("ParseCPULimit(%q): expected error, got nil", test.input)
			}
		} else {
			if err != nil {
				t.Errorf("ParseCPULimit(%q): unexpected error: %v", test.input, err)
			}
			if result != test.expected {
				t.Errorf("ParseCPULimit(%q): expected %q, got %q", test.input, test.expected, result)
			}
		}
	}
}

func TestParseMemoryLimit(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"512M", "536870912", false},
		{"1G", "1073741824", false},
		{"256K", "262144", false},
		{"max", "max", false},
		{"", "max", false},
		{"-1M", "", true},
		{"invalid", "", true},
	}

	for _, test := range tests {
		result, err := ParseMemoryLimit(test.input)
		if test.hasError {
			if err == nil {
				t.Errorf("ParseMemoryLimit(%q): expected error, got nil", test.input)
			}
		} else {
			if err != nil {
				t.Errorf("ParseMemoryLimit(%q): unexpected error: %v", test.input, err)
			}
			if result != test.expected {
				t.Errorf("ParseMemoryLimit(%q): expected %q, got %q", test.input, test.expected, result)
			}
		}
	}
}
