// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"bytes"
	"flag"
	"log"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

func TestUpgradeSourceFlagsMapDeprecatedReleaseAssetsToRegistry(t *testing.T) {
	var logBuf bytes.Buffer
	prevWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevWriter) })

	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	f := registerUpgradeSourceFlags(fs)
	if err := fs.Parse([]string{
		"-upgrade-image=docker.io/grainai/grain",
		"-upgrade-image-source=release-assets",
		"-upgrade-image-fallback=release-assets",
	}); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}
	passed := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { passed[fl.Name] = true })

	var cfg daemon.Config
	if err := f.apply(&cfg, passed); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg.UpgradeImageSource != "registry" || !cfg.UpgradeImageSourceLocked {
		t.Fatalf("UpgradeImageSource = %q (locked=%v), want registry (locked=true)", cfg.UpgradeImageSource, cfg.UpgradeImageSourceLocked)
	}
	if cfg.UpgradeImageFallback != "" || !cfg.UpgradeImageFallbackExplicit {
		t.Fatalf("UpgradeImageFallback = %q (explicit=%v), want empty (explicit=true)", cfg.UpgradeImageFallback, cfg.UpgradeImageFallbackExplicit)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "-upgrade-image-source=\"release-assets\" is deprecated") {
		t.Errorf("expected deprecation warning for -upgrade-image-source, got:\n%s", logged)
	}
	if !strings.Contains(logged, "-upgrade-image-fallback=\"release-assets\" is deprecated") {
		t.Errorf("expected deprecation warning for -upgrade-image-fallback, got:\n%s", logged)
	}
}

func TestUpgradeSourceFlagsRejectUnknownValues(t *testing.T) {
	t.Setenv("GRAIN_UPGRADE_IMAGE_SOURCE", "")
	t.Setenv("GRAIN_UPGRADE_IMAGE_FALLBACK", "")

	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	f := registerUpgradeSourceFlags(fs)
	if err := fs.Parse([]string{"-upgrade-image-source=bogus"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.resolve(map[string]bool{"upgrade-image-source": true}); err == nil {
		t.Fatal("resolve(-upgrade-image-source=bogus) succeeded, want error")
	}
}
