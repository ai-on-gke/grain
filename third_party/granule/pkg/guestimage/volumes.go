package guestimage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/bwsalmon/granule/pkg/dockercli"
)

// ParseVolumes parses a raw volume string into normalized in-guest mount points.
// It accepts JSON array strings (e.g. `["/var/log", "/data"]`) as well as
// comma-separated or space-separated paths.
func ParseVolumes(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var rawPaths []string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &rawPaths); err != nil {
			return nil, fmt.Errorf("invalid volume JSON array: %w", err)
		}
	} else {
		for _, part := range strings.Split(trimmed, ",") {
			for _, f := range strings.Fields(part) {
				if f != "" {
					rawPaths = append(rawPaths, f)
				}
			}
		}
	}
	var out []string
	for _, p := range rawPaths {
		norm, err := dockercli.NormalizeVolumePath(p)
		if err != nil {
			return nil, err
		}
		out = append(out, norm)
	}
	sort.Strings(out)
	return slices.Compact(out), nil
}

// EncodeVolumes encodes a slice of volume paths as a JSON array string.
func EncodeVolumes(volumes []string) string {
	if len(volumes) == 0 {
		return "[]"
	}
	norm := make([]string, 0, len(volumes))
	for _, v := range volumes {
		if nv, err := dockercli.NormalizeVolumePath(v); err == nil {
			norm = append(norm, nv)
		}
	}
	sort.Strings(norm)
	compact := slices.Compact(norm)
	data, _ := json.Marshal(compact)
	return string(data)
}

// DeclaredVolumes returns the volume mount points declared by ref,
// reading both the dev.granule.guest.volumes label and .Config.Volumes.
func DeclaredVolumes(ctx context.Context, dockerBinary, ref string, report io.Writer) ([]string, error) {
	if report == nil {
		report = io.Discard
	}
	d := &dockercli.CLI{Binary: dockerBinary}
	var all []string

	lbl, err := d.Label(ctx, ref, VolumesLabel)
	if err == nil && strings.TrimSpace(lbl) != "" {
		vols, err := ParseVolumes(lbl)
		if err == nil {
			all = append(all, vols...)
		} else {
			fmt.Fprintf(report, "warning: malformed %s on %s: %v\n", VolumesLabel, ref, err)
		}
	}

	dockerVols, err := d.Volumes(ctx, ref)
	if err == nil {
		for _, dv := range dockerVols {
			norm, err := dockercli.NormalizeVolumePath(dv)
			if err == nil {
				all = append(all, norm)
			}
		}
	}

	sort.Strings(all)
	return slices.Compact(all), nil
}
