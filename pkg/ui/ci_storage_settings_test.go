package ui_test

// The seven bounds on what grain CI keeps on the data disk, as the
// Settings API carries them (grain/task-317). They were daemon flags
// until the System pane started reporting what each of them was holding
// against its ceiling (cistorage_test.go, storage_test.go): a pane that
// shows "200 MiB of 256 MiB" and offers no way to move the 256 is asking
// an operator to edit a systemd unit, which is the edit that put this
// deployment into a restart loop once.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestUpdateSettingsRoundTripsTheCIStorageBounds(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	// A deployment that has chosen nothing reports zeroes, with grain's
	// own numbers beside them: the pane shows those as placeholders and
	// they are what the sweep is applying.
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.CICacheMB != 0 || read.CIArtifactMB != 0 {
		t.Errorf("unset bounds = %d MiB cache, %d MiB artifacts, want zeroes that read as grain's own",
			read.CICacheMB, read.CIArtifactMB)
	}
	defaults := ci.DefaultStorageBounds()
	if read.CICacheMBDefault != int(defaults.Cache.MaxBytes>>20) ||
		read.CICacheEntriesDefault != defaults.Cache.MaxEntries ||
		read.CICacheDaysDefault != 30 ||
		read.CIArtifactMBDefault != int(defaults.Artifacts.MaxBytes>>20) ||
		read.CIArtifactRunMBDefault != int(defaults.Artifacts.MaxRunBytes>>20) ||
		read.CIArtifactFileMBDefault != int(defaults.Artifacts.MaxArtifactBytes>>20) ||
		read.CIArtifactDaysDefault != 14 {
		t.Errorf("reported defaults = %+v, want the bounds pkg/ci actually applies", read)
	}

	// And an operator moving them: the cache widened, what one artifact
	// may be narrowed.
	cacheMB, cacheEntries, cacheDays := 1024, 8192, 60
	artifactMB, artifactRunMB, artifactFileMB, artifactDays := 4096, 256, 8, 21
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		CICacheMB: &cacheMB, CICacheEntries: &cacheEntries, CICacheDays: &cacheDays,
		CIArtifactMB: &artifactMB, CIArtifactRunMB: &artifactRunMB,
		CIArtifactFileMB: &artifactFileMB, CIArtifactDays: &artifactDays,
	}); err != nil {
		t.Fatal(err)
	}
	read, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.CICacheMB != 1024 || read.CICacheEntries != 8192 || read.CICacheDays != 60 {
		t.Errorf("cache settings = %d/%d/%d, want the saved 1024/8192/60",
			read.CICacheMB, read.CICacheEntries, read.CICacheDays)
	}
	if read.CIArtifactMB != 4096 || read.CIArtifactRunMB != 256 ||
		read.CIArtifactFileMB != 8 || read.CIArtifactDays != 21 {
		t.Errorf("artifact settings = %d/%d/%d/%d, want the saved 4096/256/8/21",
			read.CIArtifactMB, read.CIArtifactRunMB, read.CIArtifactFileMB, read.CIArtifactDays)
	}

	// Clearing one back to blank is a value, not a no-op: it is how an
	// operator goes back to grain's own number.
	zero := 0
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{CICacheMB: &zero}); err != nil {
		t.Fatal(err)
	}
	if read, err = c.GetSettings(ctx); err != nil {
		t.Fatal(err)
	} else if read.CICacheMB != 0 || read.CICacheEntries != 8192 {
		t.Errorf("after clearing the cache size = %d MiB/%d entries, want 0 and the untouched 8192",
			read.CICacheMB, read.CICacheEntries)
	}
}

// Unlike the CI history's four, none of these has a "keep it all"
// reading: what they bound is a directory on the volume this deployment
// dispatches out of, and a data disk with no ceiling on it stops the
// daemon admitting work -- which is the failure ci.DirCache and
// ci.DirArtifacts were bounded to prevent, not a trade an operator can
// usefully make. So a negative is refused here rather than stored and
// silently resolved to the default somewhere further down.
func TestUpdateSettingsRefusesAnUnboundedCIDirectory(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	stored := 512
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{CICacheMB: &stored}); err != nil {
		t.Fatal(err)
	}

	unbounded := -1
	for name, req := range map[string]ui.UpdateSettingsRequest{
		"ciCacheMb":        {CICacheMB: &unbounded},
		"ciCacheEntries":   {CICacheEntries: &unbounded},
		"ciCacheDays":      {CICacheDays: &unbounded},
		"ciArtifactMb":     {CIArtifactMB: &unbounded},
		"ciArtifactRunMb":  {CIArtifactRunMB: &unbounded},
		"ciArtifactFileMb": {CIArtifactFileMB: &unbounded},
		"ciArtifactDays":   {CIArtifactDays: &unbounded},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.UpdateSettings(ctx, req)
			if err == nil {
				t.Fatalf("UpdateSettings accepted %s = -1, want a validation error", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %v does not name %s, which is the field a form has to mark", err, name)
			}
		})
	}

	// And the refusal left the row alone, rather than half-saving the
	// request it came in.
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.CICacheMB != 512 {
		t.Errorf("cache size after the refusals = %d MiB, want the stored 512 untouched", read.CICacheMB)
	}
}
