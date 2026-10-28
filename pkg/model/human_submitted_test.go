package model

import (
	"testing"
	"time"
)

func TestHumanSubmittedTargetRoundTrips(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.FixedZone("x", 3600))
	for _, tc := range []struct{ head, target, want string }{
		{"ABCdef0123", "2026-10-25T11:00:00Z@abcdef0123", "abcdef0123"},
		{"  ", "2026-10-25T11:00:00Z", ""},
	} {
		got := HumanSubmittedTarget(at, tc.head)
		if got != tc.target {
			t.Errorf("HumanSubmittedTarget(%q) = %q, want %q", tc.head, got, tc.target)
		}
		if head := HumanSubmittedHead(got); head != tc.want {
			t.Errorf("HumanSubmittedHead(%q) = %q, want %q", got, head, tc.want)
		}
	}
}
