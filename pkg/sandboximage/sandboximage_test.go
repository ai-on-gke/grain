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

package sandboximage

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestSourceAnswersTheCustomImageOverTheStockOne(t *testing.T) {
	var s Source
	if image, changed := s.Set("ghcr.io/bwsalmon/grain/guest:sha-1", ""); image != "ghcr.io/bwsalmon/grain/guest:sha-1" || !changed {
		t.Fatalf("Set(stock, \"\") = %q, %v; want the stock image, changed", image, changed)
	}
	if s.Custom() {
		t.Error("Custom() with nothing configured, want false")
	}
	image, changed := s.Set("ghcr.io/bwsalmon/grain/guest:sha-1", " registry.example/team/guest:v3 ")
	if image != "registry.example/team/guest:v3" || !changed {
		t.Fatalf("Set(stock, custom) = %q, %v; want the custom image, changed", image, changed)
	}
	if got, err := s.Image(context.Background()); err != nil || got != "registry.example/team/guest:v3" {
		t.Fatalf("Image() = %q, %v; want the custom image", got, err)
	}
	if !s.Custom() {
		t.Error("Custom() with a custom image configured, want true")
	}
	// An upgrade moves the stock image; the custom one stays in force,
	// which is what a custom image built FROM a base is for.
	if image, changed := s.Set("ghcr.io/bwsalmon/grain/guest:sha-2", "registry.example/team/guest:v3"); image != "registry.example/team/guest:v3" || changed {
		t.Fatalf("Set after an upgrade = %q, %v; want the same custom image, unchanged", image, changed)
	}
	if image, _ := s.Set("ghcr.io/bwsalmon/grain/guest:sha-2", ""); image != "ghcr.io/bwsalmon/grain/guest:sha-2" {
		t.Fatalf("clearing the custom image answers %q, want the stock one", image)
	}
}

func TestBaseImageForBackendNamesTheMinimalBaseOfTheStockTag(t *testing.T) {
	if got, want := BaseImageForBackend("granule", "ghcr.io/bwsalmon/grain/guest:sha-abc"), "ghcr.io/bwsalmon/grain/guest-base:sha-abc"; got != want {
		t.Errorf("granule base = %q, want %q", got, want)
	}
	if got, want := BaseImageForBackend("kubernetes", "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-abc"), "ghcr.io/bwsalmon/grain/k8s-sandbox-base:sha-abc"; got != want {
		t.Errorf("kubernetes base = %q, want %q", got, want)
	}
}

func current() map[string]string {
	return map[string]string{
		ContractLabel:        strconv.Itoa(Contract),
		GranuleContractLabel: strconv.Itoa(GranuleContract),
	}
}

func TestACustomImageAtTheCurrentContractIsNotWarnedAbout(t *testing.T) {
	for _, backend := range []string{"granule", "kubernetes-granule", "kubernetes", "container"} {
		got := CheckCustomImage("registry.example/team/img:v1", backend, "base", "", current(), nil)
		if got.Warning != "" || !got.Verified || !got.Contract.OK {
			t.Errorf("%s: %+v, want a verified image with no warning", backend, got)
		}
	}
}

func TestACustomImageWithNoContractLabelSaysWhenTheBaseHasToChange(t *testing.T) {
	got := CheckCustomImage("registry.example/team/img:v1", "kubernetes", "ghcr.io/bwsalmon/grain/k8s-sandbox-base:sha-1", "", map[string]string{}, nil)
	if !got.Contract.NeedsBaseRebuild {
		t.Fatalf("an undeclared contract does not need a base rebuild: %+v", got.Contract)
	}
	for _, want := range []string{"needs rebuilding FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:sha-1", ContractLabel, "does not need rebuilding on other grain upgrades"} {
		if !strings.Contains(got.Warning, want) {
			t.Errorf("warning does not say %q:\n%s", want, got.Warning)
		}
	}
}

func TestAGranuleCustomImageIsCheckedAgainstBothContracts(t *testing.T) {
	labels := map[string]string{ContractLabel: strconv.Itoa(Contract)}
	got := CheckCustomImage("registry.example/team/guest:v1", "granule", "base", "", labels, nil)
	if got.Warning == "" || !strings.Contains(got.Warning, GranuleContractLabel) {
		t.Errorf("a VM image without %s is not warned about it: %q", GranuleContractLabel, got.Warning)
	}
	// The same labels on a pod backend are fine: there is no guest contract there.
	if got := CheckCustomImage("registry.example/team/img:v1", "kubernetes", "base", "", labels, nil); got.Warning != "" {
		t.Errorf("a pod image is warned about the Granule guest contract: %q", got.Warning)
	}
}

func TestAnUnreadableCustomImageIsReportedUnverified(t *testing.T) {
	got := CheckCustomImage("registry.example/team/img:v1", "container", "base", "", nil, errors.New("no such image"))
	if got.Verified {
		t.Error("an image whose labels could not be read is reported verified")
	}
	if !strings.Contains(got.Warning, "unverified") || !strings.Contains(got.Warning, "grain sandbox-image check") {
		t.Errorf("warning = %q, want it to say the contract is unverified and how to check it", got.Warning)
	}
}

func TestACustomImageAheadOfThisGrainAsksForAnUpgrade(t *testing.T) {
	labels := map[string]string{ContractLabel: strconv.Itoa(Contract + 1)}
	got := CheckCustomImage("registry.example/team/img:v9", "kubernetes", "base", "", labels, nil)
	if !strings.Contains(got.Warning, "Upgrade grain") {
		t.Errorf("warning = %q, want it to ask for a grain upgrade", got.Warning)
	}
}

func TestFirstLabelsFallsThroughToTheNextReader(t *testing.T) {
	failing := func(context.Context, string) (map[string]string, error) { return nil, errors.New("not local") }
	ok := func(context.Context, string) (map[string]string, error) { return map[string]string{"a": "b"}, nil }
	labels, err := FirstLabels(nil, failing, ok)(context.Background(), "img")
	if err != nil || labels["a"] != "b" {
		t.Fatalf("FirstLabels = %v, %v; want the second reader's answer", labels, err)
	}
	if _, err := FirstLabels(failing)(context.Background(), "img"); err == nil {
		t.Fatal("FirstLabels with only a failing reader succeeded")
	}
}

func TestValidateReferenceRefusesWhatIsNotOneImageReference(t *testing.T) {
	for _, ok := range []string{
		"registry.example/team/sandbox:v1",
		"localhost:5000/guest@sha256:abc",
		"us-central1-docker.pkg.dev/my-proj/sandboxes/guest:sha-0123456",
		"ubuntu",
	} {
		if err := ValidateReference(ok); err != nil {
			t.Errorf("ValidateReference(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"--privileged",
		"-v=/:/host",
		"a:v1 b:v1",
		"a:v1\tb",
		"a\x00b",
		"a\x7fb",
		"https://ghcr.io/acme/sandbox:v1",
		"http://bad",
		"docker://ghcr.io/acme/sandbox:v1",
		"img;rm",
		"img$foo",
		"img|cat",
		"img`id`",
		"ghcr.io//acme/sandbox:v1",
		"ghcr.io/acme/",
		"ghcr.io/acme:",
		"ghcr.io/acme@",
		"/leading/slash:v1",
	} {
		if err := ValidateReference(bad); err == nil {
			t.Errorf("ValidateReference(%q) = nil, want an error", bad)
		}
	}
}

func TestStaleStockPin(t *testing.T) {
	const stamped = "ghcr.io/bwsalmon/grain/guest:sha-e5544d6"
	rev := func(r string) map[string]string { return map[string]string{RevisionLabel: r} }
	for _, tc := range []struct {
		name, image, stamped string
		labels               map[string]string
		stale                bool
	}{
		{"older commit mirrored elsewhere", "us-docker.pkg.dev/example-project/grain/guest:sha-31d0973", stamped, nil, true},
		{"older commit pinned by digest", "ghcr.io/bwsalmon/grain/guest:sha-31d0973@sha256:0123", stamped, nil, true},
		{"release tag against a commit stamp", "ghcr.io/bwsalmon/grain/guest:v2.1.0", stamped, nil, true},
		{"older release", "ghcr.io/bwsalmon/grain/k8s-sandbox:v2.1.0", "ghcr.io/bwsalmon/grain/k8s-sandbox:v2.2.0", nil, true},
		{"moving tag whose revision is another build", "us-docker.pkg.dev/p/grain/guest:latest", stamped, rev("31d0973aa0b1c2d3"), true},
		{"the stamped build in a mirror", "us-docker.pkg.dev/p/grain/guest:sha-e5544d6", stamped, nil, false},
		{"moving tag at the stamped revision", "us-docker.pkg.dev/p/grain/guest:latest", stamped, rev("e5544d6aa0b1c2d3"), false},
		{"moving tag with no revision", "us-docker.pkg.dev/p/grain/guest:latest", stamped, nil, false},
		{"a team's own image", "registry.example/team/guest:sha-31d0973", stamped, nil, false},
		{"another flavour", "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-31d0973", stamped, nil, false},
		{"dev build stamped latest", "ghcr.io/bwsalmon/grain/guest:sha-31d0973", "ghcr.io/bwsalmon/grain/guest:latest", nil, false},
		{"nothing configured", "", stamped, nil, false},
		{"nothing stamped", "ghcr.io/bwsalmon/grain/guest:sha-31d0973", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := StaleStockPin(tc.image, tc.stamped, tc.labels)
			if (got != "") != tc.stale {
				t.Fatalf("StaleStockPin(%q, %q) = %q, want stale=%v", tc.image, tc.stamped, got, tc.stale)
			}
			if tc.stale && !strings.Contains(got, imageTag(tc.stamped)) {
				t.Errorf("StaleStockPin(%q) = %q, want it to name the stamped tag", tc.image, got)
			}
		})
	}
}

// A contract-current image that is the stock one at another build is
// still warned about: its contract label cannot say it was left behind.
func TestCheckCustomImageFlagsAStaleStockPinWhateverItsContract(t *testing.T) {
	labels := map[string]string{ContractLabel: ContractValue(), GranuleContractLabel: strconv.Itoa(GranuleContract)}
	got := CheckCustomImage("us-docker.pkg.dev/p/grain/guest:sha-31d0973", "granule", "base",
		"ghcr.io/bwsalmon/grain/guest:sha-e5544d6", labels, nil)
	if !got.Contract.OK || got.Stale == "" || got.Warning != got.Stale {
		t.Errorf("CheckCustomImage = %+v, want a contract-OK verdict whose warning is the stale pin", got)
	}
	got = CheckCustomImage("us-docker.pkg.dev/p/grain/guest:sha-e5544d6", "granule", "base",
		"ghcr.io/bwsalmon/grain/guest:sha-e5544d6", labels, nil)
	if got.Stale != "" || got.Warning != "" {
		t.Errorf("CheckCustomImage(mirror of the stamp) = %+v, want no warning", got)
	}
}
