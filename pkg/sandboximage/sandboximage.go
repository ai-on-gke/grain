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

// Package sandboximage decides which OCI image this installation's
// sandboxes are created from, and whether a custom one is still
// compatible with this build of grain.
//
// The stock image is a single artifact for every deployment: CI builds
// it from the repository's Granulefile (or Dockerfile.k8s-sandbox),
// pkg/cli/sandboximage.go stamps its reference into the binary, and an
// upgrade pulls it. That is what makes a deployment's daemon and its
// sandboxes provably one commit -- and it is also why nothing
// installation-specific can be in it. What a task needs inside its
// sandbox is not the same everywhere: a private CA, a language
// toolchain grain has never heard of, a package one team uses and
// nobody else does.
//
// So an installation builds its own image, FROM one of the minimal base
// images grain publishes (DefaultGuestBaseImage for Granule microVM
// backends, DefaultK8sSandboxBaseImage for container and Kubernetes pod
// backends), and names it in Settings (model.Config.SandboxImage). Those
// base images carry only the boundary grain relies on inside a sandbox,
// and that boundary is versioned (Contract, contract.go): a custom image
// is stamped with the contract of the base it was built from, so it needs
// rebuilding only when that contract falls out of
// [MinimumContract, Contract] -- not on every grain upgrade, which is the
// whole point of building it FROM a base rather than FROM the full stock
// image of one commit.
//
// Source holds the answer the sandbox backends ask for on each create;
// CheckCustomImage is the verdict an operator sees about a custom image.
package sandboximage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Source is which image new sandboxes are created from right now: the
// stock image this process resolved for its backend, or the custom one
// this deployment configured in its place.
//
// One per daemon. Set is called at startup and again on every reconcile
// tick (the daemon's liveConfig), so an image changed in Settings, or in
// a pull request merged into the state repository, reaches the next
// sandbox created rather than the next restart. A sandbox already running
// keeps the image it was created from.
//
// Unlike the setup-script customizer this replaced, nothing is ever built
// here: the image is pulled by whatever creates the sandbox, exactly as
// the stock one is, so a reconcile tick and a dispatch cost a mutex.
type Source struct {
	mu     sync.Mutex
	stock  string
	custom string
}

// Set records the stock image and the custom image configured over it
// ("" for none), and reports the image sandboxes are now created from
// and whether that differs from what it was.
func (s *Source) Set(stock, custom string) (image string, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.currentLocked()
	s.stock, s.custom = strings.TrimSpace(stock), strings.TrimSpace(custom)
	after := s.currentLocked()
	return after, after != before
}

// Image is the image the next sandbox is created from. It never blocks
// and never fails; the signature is the resolver orchestrator.GranuleConfig
// asks on each create.
func (s *Source) Image(context.Context) (string, error) {
	return s.Current(), nil
}

// Current is Image without the resolver's signature.
func (s *Source) Current() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLocked()
}

// Custom reports whether a custom image is in force over the stock one.
func (s *Source) Custom() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.custom != "" && s.custom != s.stock
}

func (s *Source) currentLocked() string {
	if s.custom != "" {
		return s.custom
	}
	return s.stock
}

// BaseImageForBackend is the minimal base image a custom image for
// backend is built FROM, carrying the same tag as stockImage (the full
// image this build is stamped for on that backend family) so the two
// are from one commit.
func BaseImageForBackend(backend, stockImage string) string {
	if IsGranuleBackend(backend) {
		return BaseImageFor(stockImage, "guest-base", DefaultGuestBaseImage)
	}
	return BaseImageFor(stockImage, "k8s-sandbox-base", DefaultK8sSandboxBaseImage)
}

// CustomImageCheck is the verdict on a configured custom sandbox image:
// whether its contract labels could be read at all, what they say, and
// the one warning an operator should see about it ("" when there is
// nothing to say).
type CustomImageCheck struct {
	Image     string `json:"image"`
	Backend   string `json:"backend"`
	BaseImage string `json:"baseImage"`
	// Verified is whether the image's labels were read. False is not a
	// verdict that the image is incompatible, only that grain could not
	// tell -- an image not yet pulled to this host and a registry that
	// wants credentials both end up here.
	Verified bool          `json:"verified"`
	Contract ContractCheck `json:"contract"`
	Warning  string        `json:"warning,omitempty"`
}

// CheckCustomImage judges a custom image from its OCI labels (as a
// LabelReader read them, or the error it failed with).
//
// It is deliberately a contract check and not the commit check the stock
// image gets (the daemon's sandboxImageDriftWarning): a custom image is
// expected to be from a different commit than the daemon -- it was built
// FROM a base image once and is meant to stay put across upgrades. What
// it must not be is built against a sandbox boundary this grain no longer
// drives, which is what its contract label says.
func CheckCustomImage(image, backend, baseImage string, labels map[string]string, readErr error) CustomImageCheck {
	out := CustomImageCheck{Image: image, Backend: backend, BaseImage: baseImage}
	if readErr != nil {
		out.Contract = CheckImageLabels(nil, backend)
		out.Warning = fmt.Sprintf("Could not read the labels of custom sandbox image %s (%v), so its sandbox contract is unverified. "+
			"This grain drives images at %s %s%s. Check it with `grain sandbox-image check %s`.",
			image, readErr, ContractLabel, expectedRange(MinimumContract, Contract), granuleRangeNote(backend), image)
		return out
	}
	out.Verified = true
	out.Contract = CheckImageLabels(labels, backend)
	c := out.Contract
	switch {
	case c.State == ContractAhead || c.GranuleState == ContractAhead && IsGranuleBackend(backend):
		out.Warning = fmt.Sprintf("Custom sandbox image %s was built against a newer sandbox contract than this grain knows (%s). "+
			"Upgrade grain, or rebuild the image FROM a base image of this grain's own version (%s).", image, c.Summary, baseImage)
	case c.NeedsBaseRebuild:
		out.Warning = fmt.Sprintf("Custom sandbox image %s needs rebuilding FROM %s: %s. "+
			"The sandbox contract is what grain relies on inside every sandbox (the debian user and home layout, git, curl and CA "+
			"certificates, python3 for capability port forwarding, and on VM backends systemd, sudo, grain-connect and Granule's "+
			"guest contract). It changes only when that boundary does, and an image built at a contract older than v%d -- or "+
			"one that declares none, which is an image not built FROM a grain base image -- may be missing something grain "+
			"needs. A custom image does not need rebuilding on other grain upgrades.",
			image, baseImage, c.Summary, MinimumContract)
	case c.ImageContract < Contract || IsGranuleBackend(backend) && c.GranuleContract < GranuleContract:
		out.Warning = fmt.Sprintf("Custom sandbox image %s is at an older sandbox contract than this grain's current one (%s). "+
			"It is still supported; rebuild it FROM %s before a grain upgrade raises the minimum past it.", image, c.Summary, baseImage)
	}
	return out
}

func granuleRangeNote(backend string) string {
	if !IsGranuleBackend(backend) {
		return ""
	}
	return fmt.Sprintf(" and %s %s", GranuleContractLabel, expectedRange(GranuleMinimumContract, GranuleContract))
}

// LabelReader reads an OCI image's labels.
type LabelReader func(ctx context.Context, image string) (map[string]string, error)

// DockerLabels is a LabelReader that asks a docker daemon about an image
// it already has (`docker image inspect`), pulling nothing. dockerBin is
// "docker" when empty.
func DockerLabels(dockerBin string) LabelReader {
	if strings.TrimSpace(dockerBin) == "" {
		dockerBin = "docker"
	}
	return func(ctx context.Context, image string) (map[string]string, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, dockerBin, "image", "inspect", "--format", "{{json .Config.Labels}}", "--", image)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("docker image inspect: %s", msg)
			}
			return nil, fmt.Errorf("docker image inspect: %w", err)
		}
		labels := map[string]string{}
		if trimmed := strings.TrimSpace(stdout.String()); trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal([]byte(trimmed), &labels); err != nil {
				return nil, fmt.Errorf("parsing the labels docker reported: %w", err)
			}
		}
		return labels, nil
	}
}

// FirstLabels is a LabelReader asking each reader in turn, answering with
// the first that succeeds -- a local docker inspect before a registry
// round trip -- or the last error.
func FirstLabels(readers ...LabelReader) LabelReader {
	return func(ctx context.Context, image string) (map[string]string, error) {
		err := fmt.Errorf("no way to read image labels is configured")
		for _, r := range readers {
			if r == nil {
				continue
			}
			var labels map[string]string
			if labels, err = r(ctx, image); err == nil {
				return labels, nil
			}
		}
		return nil, err
	}
}

// ValidateReference reports whether image can be handed on as one OCI
// image reference. The custom image reaches `docker run`, `docker image
// inspect` and granule's -kontur-image as a positional argument, and it
// can arrive from the state repository as well as from Settings, so a
// value that is several words, carries a control character or shell
// metacharacter, is a URL with a scheme, or begins with "-" (which docker
// would parse as one of its own flags) is refused rather than passed on.
func ValidateReference(image string) error {
	if image == "" {
		return fmt.Errorf("an image reference is required")
	}
	if strings.HasPrefix(image, "-") {
		return fmt.Errorf("%q begins with \"-\", so a command would read it as a flag", image)
	}
	if strings.Contains(image, "://") {
		return fmt.Errorf("%q is a URL, not an OCI image reference (drop the scheme, e.g. registry.example/team/sandbox:v1)", image)
	}
	for _, r := range image {
		if r <= ' ' || r == 0x7f || !isImageReferenceChar(r) {
			return fmt.Errorf("%q is not one OCI image reference, such as registry.example/team/sandbox:v1", image)
		}
	}
	first := image[0]
	last := image[len(image)-1]
	if !isASCIIAlnum(first) || strings.Contains(image, "//") || last == '/' || last == ':' || last == '@' {
		return fmt.Errorf("%q is not one OCI image reference, such as registry.example/team/sandbox:v1", image)
	}
	return nil
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isImageReferenceChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '.' || r == '_' || r == '/' || r == ':' || r == '@' || r == '-'
}
