package daemon

import (
	"slices"
	"testing"
)

// A task resumed after its model failed tries the rest of its rotation
// first, and keeps the failed models at the back rather than losing them.
func TestAvoidLastResumesOnTheNextModel(t *testing.T) {
	got := avoidLast([]string{"a", "b", "c"}, []string{"a"})
	if !slices.Equal(got, []string{"b", "c", "a"}) {
		t.Fatalf("avoidLast = %v, want [b c a]", got)
	}
	if got := avoidLast([]string{"a", "b"}, []string{"b", "a"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("every model avoided: %v, want the list as it was", got)
	}
	if got := avoidLast([]string{"a"}, nil); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("nothing avoided: %v", got)
	}
}
