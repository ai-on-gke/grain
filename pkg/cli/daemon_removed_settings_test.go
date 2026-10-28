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
	"strings"
	"testing"
)

func clearRemovedRelayAndTunnelEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{
		"GRAIN_TUNNEL_GATEWAY_URL",
		"GRAIN_TUNNEL_TOKEN",
		"GRAIN_RELAY_URL",
		"GRAIN_RELAY_AUTH_TOKEN_FILE",
		"GRAIN_RELAY_AUTH_TOKEN",
		"GRAIN_RELAY_TOKEN",
		"GRAIN_RELAY_AUDIENCE",
		"GRAIN_RELAY_IAP_JWT_AUDIENCE",
		"GRAIN_RELAY_SESSION_LIFETIME",
	} {
		t.Setenv(env, "")
	}
}

func TestDaemonRefusesRemovedTunnelGateway(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{name: "flag", args: []string{"-tunnel-gateway-url", "https://tunnel.example.com"}, want: "-tunnel-gateway-url"},
		{name: "token flag", args: []string{"-tunnel-token", "s"}, want: "-tunnel-token"},
		{name: "url env", env: map[string]string{"GRAIN_TUNNEL_GATEWAY_URL": "https://tunnel.example.com"}, want: "$GRAIN_TUNNEL_GATEWAY_URL"},
		{name: "token env", env: map[string]string{"GRAIN_TUNNEL_TOKEN": "s"}, want: "$GRAIN_TUNNEL_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearRemovedRelayAndTunnelEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, _, _, err := parseDaemonArgs(tc.args)
			if err == nil {
				t.Fatal("parseDaemonArgs accepted a removed tunnel gateway setting")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "the tunnel gateway has been removed") {
				t.Errorf("err = %q, want it to name %s and say the tunnel gateway has been removed", err, tc.want)
			}
		})
	}
}

func TestDaemonRefusesRemovedRelaySettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{name: "relay-url flag", args: []string{"-relay-url", "https://relay.example.com"}, want: "-relay-url"},
		{name: "relay-auth-token-file flag", args: []string{"-relay-auth-token-file", "/etc/grain/token"}, want: "-relay-auth-token-file"},
		{name: "relay-token flag", args: []string{"-relay-token", "secret"}, want: "-relay-token"},
		{name: "relay-audience flag", args: []string{"-relay-audience", "aud"}, want: "-relay-audience"},
		{name: "relay-iap-jwt-audience flag", args: []string{"-relay-iap-jwt-audience", "https://relay.example.com/*"}, want: "-relay-iap-jwt-audience"},
		{name: "relay-session-lifetime flag", args: []string{"-relay-session-lifetime", "4m"}, want: "-relay-session-lifetime"},
		{name: "unknown relay flag", args: []string{"-relay-custom-flag", "val"}, want: "-relay-custom-flag"},
		{name: "GRAIN_RELAY_URL env", env: map[string]string{"GRAIN_RELAY_URL": "https://relay.example.com"}, want: "$GRAIN_RELAY_URL"},
		{name: "GRAIN_RELAY_AUTH_TOKEN_FILE env", env: map[string]string{"GRAIN_RELAY_AUTH_TOKEN_FILE": "/etc/grain/token"}, want: "$GRAIN_RELAY_AUTH_TOKEN_FILE"},
		{name: "GRAIN_RELAY_AUTH_TOKEN env", env: map[string]string{"GRAIN_RELAY_AUTH_TOKEN": "secret"}, want: "$GRAIN_RELAY_AUTH_TOKEN"},
		{name: "GRAIN_RELAY_TOKEN env", env: map[string]string{"GRAIN_RELAY_TOKEN": "secret"}, want: "$GRAIN_RELAY_TOKEN"},
		{name: "GRAIN_RELAY_AUDIENCE env", env: map[string]string{"GRAIN_RELAY_AUDIENCE": "aud"}, want: "$GRAIN_RELAY_AUDIENCE"},
		{name: "GRAIN_RELAY_IAP_JWT_AUDIENCE env", env: map[string]string{"GRAIN_RELAY_IAP_JWT_AUDIENCE": "https://relay.example.com/*"}, want: "$GRAIN_RELAY_IAP_JWT_AUDIENCE"},
		{name: "GRAIN_RELAY_SESSION_LIFETIME env", env: map[string]string{"GRAIN_RELAY_SESSION_LIFETIME": "4m"}, want: "$GRAIN_RELAY_SESSION_LIFETIME"},
		{name: "other GRAIN_RELAY_ env", env: map[string]string{"GRAIN_RELAY_EXTRA": "1"}, want: "$GRAIN_RELAY_EXTRA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearRemovedRelayAndTunnelEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, _, _, err := parseDaemonArgs(tc.args)
			if err == nil {
				t.Fatal("parseDaemonArgs accepted a removed relay setting")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "the relay has been removed") {
				t.Errorf("err = %q, want it to name %s and say the relay has been removed", err, tc.want)
			}
		})
	}
}
