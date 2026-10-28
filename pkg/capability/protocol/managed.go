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

package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
)

// This file is the configure fields Grain fills in itself (§4.5): secret
// fields whose value only Grain and the server ever read -- the
// data-plane bearer token Grain's own proxy sends a stdio server's
// loopback listener -- so nobody has to invent one. Grain generates each
// with a CSPRNG the first time a server declaring it is configured,
// keeps it in the encrypted secrets store under ManagedSecretName, and
// sends it the way a configured value is sent. A value the operator set
// in the config still wins.

// ManagedFields are the configure fields Grain manages when a server's
// schema declares them as secret fields. Every field here is consumed by
// Grain and the server alone; a credential somebody else issues (an API
// key, a service-account key, a webhook URL) is never one of them.
var ManagedFields = []string{DataPlaneTokenConfig}

// ManagedTokenBytes is how many random bytes a managed value is made
// of, before encoding: at least the 32 a data-plane listener requires.
const ManagedTokenBytes = 32

// managedKeys is the secrets-store key each managed field's value is
// kept under, inside the server's managed secret.
var managedKeys = map[string]string{DataPlaneTokenConfig: "data-plane-token"}

// ManagedSecretName is the secrets-store name (<secret>/<key>) the value
// Grain generates for capabilityID's managed field is kept under:
// deterministic, so the same value is found again after a restart, and
// named for the capability_id without carrying it, since a capability_id
// may be a URL and a secret name may not hold a "/".
func ManagedSecretName(capabilityID, field string) string {
	key, ok := managedKeys[field]
	if !ok {
		key = field
	}
	sum := sha256.Sum256([]byte(capabilityID))
	return "capability-managed-" + hex.EncodeToString(sum[:4]) + "/" + key
}

// ManagedSecretFields are the fields of schema Grain manages: those of
// ManagedFields the schema declares as secret fields.
func ManagedSecretFields(schema map[string]any) []string {
	var out []string
	for _, f := range SecretFields(schema) {
		if slices.Contains(ManagedFields, f) {
			out = append(out, f)
		}
	}
	return out
}

// ManagedSecrets is the store managed values are kept in. Ensure
// returns nil once name holds a value, generating one with generate when
// it holds none; it never returns the value. Rotate replaces whatever
// name holds with a fresh one.
type ManagedSecrets interface {
	Ensure(name string, generate func() (string, error)) error
	Rotate(name string, generate func() (string, error)) error
}

// GenerateManagedValue is a fresh managed value: ManagedTokenBytes from
// crypto/rand, base64url-encoded without padding (43 bytes).
func GenerateManagedValue() (string, error) {
	b := make([]byte, ManagedTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating a managed secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// WithManagedSecrets is config with every managed field of schema the
// config leaves unset pointed at its managed secret, which store is made
// to hold first. A field the config sets -- an operator's override, or a
// deployment's token from before Grain managed it -- is left as it is. A
// nil store manages nothing: the server falls back to whatever it reads
// from its environment, as before.
func WithManagedSecrets(capabilityID string, schema, config map[string]any, store ManagedSecrets) (map[string]any, error) {
	if store == nil {
		return config, nil
	}
	fields := ManagedSecretFields(schema)
	if len(fields) == 0 {
		return config, nil
	}
	out := maps.Clone(config)
	if out == nil {
		out = map[string]any{}
	}
	for _, f := range fields {
		if v, set := out[f]; set && v != "" && v != nil {
			continue
		}
		name := ManagedSecretName(capabilityID, f)
		if err := store.Ensure(name, GenerateManagedValue); err != nil {
			return nil, fmt.Errorf("config.%s: grain could not generate it: %v", f, err)
		}
		out[f] = SecretRef(name)
	}
	return out, nil
}

// ManagedOverridden reports whether config sets field itself rather
// than leaving it to Grain: anything but its managed secret's reference.
func ManagedOverridden(capabilityID, field string, config map[string]any) bool {
	v, set := config[field]
	if !set || v == "" || v == nil {
		return false
	}
	return v != SecretRef(ManagedSecretName(capabilityID, field))
}
