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
	"fmt"
	"os"
	"slices"
	"strings"
)

// removedTunnelGatewaySettings refuses the settings of the tunnel gateway
// (grain-tunnel-gateway), which is gone (grain/task-2099). Ignoring them
// would start a daemon its operator can no longer reach.
// passed is the set of flags given on the command line.
func removedTunnelGatewaySettings(passed map[string]bool) error {
	var set []string
	for _, name := range []string{"tunnel-gateway-url", "tunnel-token"} {
		if passed[name] {
			set = append(set, "-"+name)
		}
	}
	for _, env := range []string{"GRAIN_TUNNEL_GATEWAY_URL", "GRAIN_TUNNEL_TOKEN"} {
		if os.Getenv(env) != "" {
			set = append(set, "$"+env)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("%s: the tunnel gateway has been removed (docs/relay-removed.md). "+
		"Remove %s to start", strings.Join(set, ", "), strings.Join(set, " and "))
}

// removedRelaySettings refuses the settings of the rendezvous relay
// (cmd/grain-relay, pkg/relay), which has been removed (docs/relay-removed.md).
// Ignoring them would start a daemon its operator can no longer reach through
// the relay URL they configured. passed is the set of flags given on the
// command line; unknown are any unrecognised flags splitDaemonFlags set aside.
func removedRelaySettings(passed map[string]bool, unknown []string) error {
	var set []string
	for _, name := range []string{
		"relay-url",
		"relay-auth-token-file",
		"relay-token",
		"relay-audience",
		"relay-iap-jwt-audience",
		"relay-session-lifetime",
	} {
		if passed[name] {
			set = append(set, "-"+name)
		}
	}
	for _, u := range unknown {
		name := strings.TrimLeft(u, "-")
		if strings.HasPrefix(name, "relay-") && !slices.Contains(set, "-"+name) {
			set = append(set, "-"+name)
		}
	}
	knownEnv := []string{
		"GRAIN_RELAY_URL",
		"GRAIN_RELAY_AUTH_TOKEN_FILE",
		"GRAIN_RELAY_AUTH_TOKEN",
		"GRAIN_RELAY_TOKEN",
		"GRAIN_RELAY_AUDIENCE",
		"GRAIN_RELAY_IAP_JWT_AUDIENCE",
		"GRAIN_RELAY_SESSION_LIFETIME",
	}
	for _, env := range knownEnv {
		if os.Getenv(env) != "" {
			set = append(set, "$"+env)
		}
	}
	var extraEnv []string
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" || !strings.HasPrefix(k, "GRAIN_RELAY_") {
			continue
		}
		if !slices.Contains(knownEnv, k) && !slices.Contains(extraEnv, "$"+k) {
			extraEnv = append(extraEnv, "$"+k)
		}
	}
	slices.Sort(extraEnv)
	set = append(set, extraEnv...)
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("%s: the relay has been removed (see docs/relay-removed.md). "+
		"Remove %s to start", strings.Join(set, ", "), strings.Join(set, " and "))
}
