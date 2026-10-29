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

package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SecretMount mounts one Secret read-only into the sandbox container --
// and only that container: no init container is given it.
type SecretMount struct {
	// Secret is the Secret's name.
	Secret string
	// MountPath is the directory its keys appear under, one file each.
	MountPath string
}

// secretVolumeName is the volume a SecretMount is carried by.
func secretVolumeName(i int) string { return fmt.Sprintf("secret-%d", i) }

// PodUID reads pod name's metadata.uid, which an ownerReference to it
// needs.
func (c Client) PodUID(ctx context.Context, name string) (string, error) {
	out, err := c.run(ctx, "get", "pod", name, "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return "", fmt.Errorf("k8s: reading pod %q's uid: %w", name, err)
	}
	uid := strings.TrimSpace(out)
	if uid == "" {
		return "", fmt.Errorf("k8s: pod %q has no uid", name)
	}
	return uid, nil
}

// SecretManifest is the Opaque Secret name holding data, owned by pod
// ownerPod (uid ownerUID) so that it is garbage-collected with the pod
// and never outlives it.
func (c Client) SecretManifest(name, ownerPod, ownerUID string, data map[string][]byte) ([]byte, error) {
	if name == "" || ownerPod == "" || ownerUID == "" {
		return nil, fmt.Errorf("k8s: a sandbox secret needs a name and an owning pod")
	}
	encoded := map[string]any{}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		encoded[k] = base64.StdEncoding.EncodeToString(data[k])
	}
	labels := map[string]any{ManagedByLabel: ManagedByValue}
	if c.Instance != "" {
		labels[InstanceLabel] = c.Instance
	}
	return json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "Opaque",
		"metadata": map[string]any{
			"name":      name,
			"namespace": c.NamespaceOrDefault(),
			"labels":    labels,
			"ownerReferences": []any{map[string]any{
				"apiVersion":         "v1",
				"kind":               "Pod",
				"name":               ownerPod,
				"uid":                ownerUID,
				"blockOwnerDeletion": false,
			}},
		},
		"immutable": true,
		"data":      encoded,
	})
}

// CreateSecret creates SecretManifest's Secret. It uses create, not
// apply, so that the Role needs only secrets: create (and delete, for
// DeleteSecret) -- never get, list or patch.
func (c Client) CreateSecret(ctx context.Context, name, ownerPod, ownerUID string, data map[string][]byte) error {
	manifest, err := c.SecretManifest(name, ownerPod, ownerUID, data)
	if err != nil {
		return err
	}
	if _, err := c.runInput(ctx, string(manifest), "create", "-f", "-"); err != nil {
		return fmt.Errorf("k8s: creating secret %q: %w", name, err)
	}
	return nil
}

// DeleteSecret deletes Secret name; one already gone is not an error.
func (c Client) DeleteSecret(ctx context.Context, name string) error {
	if _, err := c.run(ctx, "delete", "secret", name, "--ignore-not-found", "--wait=false"); err != nil {
		return fmt.Errorf("k8s: deleting secret %q: %w", name, err)
	}
	return nil
}
