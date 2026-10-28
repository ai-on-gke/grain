package antigravity

import "testing"

func TestProviderFor(t *testing.T) {
	tests := []struct {
		key      string
		expected string
	}{
		{"", ""},
		{"ya29.a0AfB_bCG...", "vertex"},
		{" ya29.foo ", "vertex"},
		{"{\"type\": \"service_account\"}", "vertex"},
		{" { \"type\":... }", "vertex"},
		{"AIzaSy...", "gemini"},
		{"some_other_key", "gemini"},
	}

	for _, tt := range tests {
		got := providerFor(tt.key)
		if got != tt.expected {
			t.Errorf("ProviderFor(%q) = %q; want %q", tt.key, got, tt.expected)
		}
	}
}
