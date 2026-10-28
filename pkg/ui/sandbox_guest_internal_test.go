package ui

import "testing"

// A release stamps its guest by digest as well as by tag
// (scripts/release-images.sh), and the tag in front of the digest still
// says which commit it was built from.
func TestGuestImageCommitReadsThroughADigest(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for image, want := range map[string]string{
		"ghcr.io/bwsalmon/grain/guest:sha-7746d62":           "7746d62",
		"ghcr.io/bwsalmon/grain/guest:sha-7746d62@" + digest: "7746d62",
		"ghcr.io/bwsalmon/grain/guest@" + digest:             "",
		"ghcr.io/bwsalmon/grain/guest:main@" + digest:        "",
		"localhost:5000/bwsalmon/grain/guest:sha-abc1234":    "abc1234",
		"localhost:5000/bwsalmon/grain/guest@" + digest:      "",
	} {
		if got := guestImageCommit(image); got != want {
			t.Errorf("guestImageCommit(%q) = %q, want %q", image, got, want)
		}
	}
}
