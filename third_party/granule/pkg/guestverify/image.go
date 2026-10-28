package guestverify

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/bwsalmon/granule/pkg/guestboot"
)

// imageID resolves a reference to the content digest of the image it
// names, pulling it if the daemon does not have it yet.
//
// The digest rather than the reference is what the verdict is keyed on:
// a tag is a name somebody can move, and a cache keyed on "granule:latest"
// would answer for whatever that used to be. It is also resolved *before*
// anything boots, so a verify and the cache entry it writes are about the
// same bytes even if the tag moves underneath them while the VM runs.
func imageID(ctx context.Context, cli *guestboot.CLI, ref string) (string, error) {
	id, err := cli.Output(ctx, "image", "inspect", "-f", "{{.Id}}", ref)
	if err == nil {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			return trimmed, nil
		}
	}
	// Not local yet. Pulling here rather than leaving it to `docker run`
	// means the digest is known before the boot, and that an image that
	// cannot be fetched says so in one line instead of as a failed VM.
	if err := cli.Run(ctx, io.Discard, "pull", ref); err != nil {
		return "", fmt.Errorf("resolving %s: it is not in the local daemon and could not be pulled: %w", ref, err)
	}
	id, err = cli.Output(ctx, "image", "inspect", "-f", "{{.Id}}", ref)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", ref, err)
	}
	if trimmed := strings.TrimSpace(id); trimmed != "" {
		return trimmed, nil
	}
	return "", fmt.Errorf("resolving %s: docker reported no image ID for it", ref)
}
