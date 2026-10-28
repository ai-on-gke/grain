package model

import (
	"reflect"
	"testing"
)

func TestSizeBucketsHoldWhatTheyClaimTo(t *testing.T) {
	for _, tc := range []struct {
		n      int64
		bucket int
		max    int64
	}{
		{0, 0, 0},
		{1, 1, 1},
		{2, 2, 3},
		{3, 2, 3},
		{4, 3, 7},
		{64 * 1024, 17, 131071},
		{65535, 16, 65535},
	} {
		if got := SizeBucket(tc.n); got != tc.bucket {
			t.Errorf("SizeBucket(%d) = %d, want %d", tc.n, got, tc.bucket)
		}
		if got := SizeBucketMax(tc.bucket); got != tc.max {
			t.Errorf("SizeBucketMax(%d) = %d, want %d", tc.bucket, got, tc.max)
		}
		// The bound a percentile drawn from this histogram claims has to
		// actually hold for every sample in the bucket, or "at most" is a
		// lie rather than a rounding.
		if tc.n > SizeBucketMax(SizeBucket(tc.n)) {
			t.Errorf("%d is above its own bucket's maximum %d", tc.n, SizeBucketMax(SizeBucket(tc.n)))
		}
	}
}

func TestSizeHistogramRoundTripsThroughItsColumn(t *testing.T) {
	h := SizeHistogram{}
	for _, n := range []int64{0, 1, 1, 900, 900, 70000} {
		h.Add(n)
	}
	if got, want := h.Total(), 6; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
	encoded := h.Encode()
	back := DecodeSizeHistogram(encoded)
	if !reflect.DeepEqual(map[int]int(back), map[int]int(h)) {
		t.Errorf("decode(%q) = %v, want %v", encoded, back, h)
	}
	if got := (SizeHistogram{}).Encode(); got != "" {
		t.Errorf("an empty histogram encoded as %q, want the empty column", got)
	}
	if got := DecodeSizeHistogram(""); len(got) != 0 {
		t.Errorf("decoding an empty column gave %v, want nothing", got)
	}
}

// TestDecodeSizeHistogramDropsWhatItCannotRead: a report that refused to
// render because one historical row was malformed would be worse than one
// that measures the rest, so a bad pair is skipped and its neighbours
// still count.
func TestDecodeSizeHistogramDropsWhatItCannotRead(t *testing.T) {
	got := DecodeSizeHistogram("3:4,rubbish,7:x,-1:2,9:5")
	want := SizeHistogram{3: 4, 9: 5}
	if !reflect.DeepEqual(map[int]int(got), map[int]int(want)) {
		t.Errorf("decoded %v, want %v", got, want)
	}
}

func TestSizeHistogramMergeSumsBuckets(t *testing.T) {
	a := SizeHistogram{2: 1, 5: 3}
	a.Merge(SizeHistogram{5: 2, 8: 1})
	want := SizeHistogram{2: 1, 5: 5, 8: 1}
	if !reflect.DeepEqual(map[int]int(a), map[int]int(want)) {
		t.Errorf("merged = %v, want %v", a, want)
	}
	if got, want := a.Buckets(), []int{2, 5, 8}; !reflect.DeepEqual(got, want) {
		t.Errorf("Buckets() = %v, want %v", got, want)
	}
}

func TestRunTelemetryEmpty(t *testing.T) {
	if !(RunTelemetry{}).Empty() {
		t.Error("a run that called nothing should record nothing")
	}
	if (RunTelemetry{Tools: []RunToolUse{{Tool: "run_command"}}}).Empty() {
		t.Error("a run with a census is not empty")
	}
	if (RunTelemetry{CheckWaits: []RunCheckWait{{Seq: 1}}}).Empty() {
		t.Error("a run with a CI wait is not empty")
	}
}
