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

//go:build ignore

// adc-probe [config]: the Google client library's own reading of a
// token-mode grant. It loads the placed external_account config the way
// Application Default Credentials does (golang.org/x/oauth2/google, the
// library gcloud's Go tools and the Go client libraries use), asks its
// token source for a token, and prints the token's prefix and expiry --
// so the exchange at the forwarded listener is the library's, not a
// hand-written curl's. Built static and copied into the run's container:
//
//	CGO_ENABLED=0 go build -o adc-probe docs/test-runs/2026-11-08-gcp-token-mode/adc-probe.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

func main() {
	path := os.Getenv("HOME") + "/.gcp-service-account.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("adc-probe:", err)
		os.Exit(1)
	}
	ctx := context.Background()
	creds, err := google.CredentialsFromJSONWithType(ctx, b, google.ExternalAccount, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		fmt.Println("adc-probe: loading", path+":", err)
		os.Exit(1)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		fmt.Println("adc-probe: token:", err)
		os.Exit(1)
	}
	prefix, _, _ := strings.Cut(tok.AccessToken, "@")
	fmt.Printf("adc-probe: %s gave a %s token %s@<cut>, expiring in %s\n", path, tok.TokenType, prefix, time.Until(tok.Expiry).Round(time.Second))
}
