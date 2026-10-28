package hostinit

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// The grain-sandbox Service must publish the daemon pod before its
// readinessProbe passes: the daemon dispatches as soon as it starts, and
// a sandbox created in those first seconds otherwise clones from a
// cluster IP with no endpoint behind it
// (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F2).
func TestGrainSandboxServicePublishesTheDaemonBeforeItIsReady(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "kubernetes", "grain.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				PublishNotReadyAddresses bool `yaml:"publishNotReadyAddresses"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decoding grain.yaml: %v", err)
		}
		if doc.Kind == "Service" && doc.Metadata.Name == "grain-sandbox" {
			if !doc.Spec.PublishNotReadyAddresses {
				t.Fatal("the grain-sandbox Service does not set publishNotReadyAddresses: true")
			}
			return
		}
	}
	t.Fatal("grain.yaml has no grain-sandbox Service")
}
