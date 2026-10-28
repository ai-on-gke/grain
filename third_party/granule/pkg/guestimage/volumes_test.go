package guestimage

import (
	"reflect"
	"testing"
)

func TestParseVolumes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"json array", `["/var/log", "/data"]`, []string{"/data", "/var/log"}},
		{"comma separated", `/data,/var/log`, []string{"/data", "/var/log"}},
		{"space separated", `/data /var/log`, []string{"/data", "/var/log"}},
		{"mixed duplicates and trailing slashes", `["/var/log/", "/var/log", "/data"]`, []string{"/data", "/var/log"}},
		{"empty string", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVolumes(tc.raw)
			if err != nil {
				t.Fatalf("ParseVolumes(%q) = %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseVolumes(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestEncodeVolumes(t *testing.T) {
	got := EncodeVolumes([]string{"/var/log/", "/data", "/data"})
	want := `["/data","/var/log"]`
	if got != want {
		t.Errorf("EncodeVolumes() = %q, want %q", got, want)
	}

	if gotEmpty := EncodeVolumes(nil); gotEmpty != "[]" {
		t.Errorf("EncodeVolumes(nil) = %q, want []", gotEmpty)
	}
}

func TestParseVolumes_Errors(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"invalid JSON", `["/unclosed`},
		{"relative path in JSON", `["relative/path"]`},
		{"relative path in list", `var/log, /data`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseVolumes(tc.raw)
			if err == nil {
				t.Fatalf("ParseVolumes(%q) succeeded, want error", tc.raw)
			}
		})
	}
}
