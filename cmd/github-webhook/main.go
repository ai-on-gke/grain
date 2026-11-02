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

// Command github-webhook receives GitHub App webhook deliveries on a public
// endpoint, verifies GitHub's X-Hub-Signature-256 HMAC, deduplicates and
// persists compact normalized envelopes, and serves them over a signed
// internal endpoint to grain daemon.
//
// Like cmd/checkpost, it runs as its own unprivileged binary with zero
// GitHub credentials and zero access to grain's internal SQLite/Postgres
// store or sandbox network (design/design/architecture.design.md). On GKE, that lets
// Service/grain stay protected behind Google Cloud IAP + access-code while
// Service/grain-webhook sits behind a non-IAP BackendConfig on
// /webhooks/github alone.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/ghwebhook"
)

const (
	pruneInterval       = time.Hour
	webhookSecretEnvKey = "GITHUB_WEBHOOK_SECRET"
	relaySecretEnvKey   = "GITHUB_WEBHOOK_RELAY_SECRET"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "github-webhook: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "github-webhook: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `github-webhook receives GitHub App webhooks and relays verified events to grain daemon.

  github-webhook serve -addr :8080 -store /data/events.jsonl

Reads GitHub's webhook secret from `+webhookSecretEnvKey+` and the internal daemon relay
secret from `+relaySecretEnvKey+`.
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "address to listen on")
	storePath := fs.String("store", "", "JSONL file to persist verified webhook deliveries in")
	if err := fs.Parse(args); err != nil {
		return err
	}

	whSecret := os.Getenv(webhookSecretEnvKey)
	if whSecret == "" {
		return fmt.Errorf("%s is not set, so GitHub webhook deliveries cannot be verified", webhookSecretEnvKey)
	}
	relaySecret := os.Getenv(relaySecretEnvKey)
	if relaySecret == "" {
		return fmt.Errorf("%s is not set, so grain daemon reads cannot be authenticated", relaySecretEnvKey)
	}
	if *storePath == "" {
		fmt.Fprintln(os.Stderr, "github-webhook: no -store given; events are held in memory only and lost on restart")
	}

	st, err := ghwebhook.OpenStore(*storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := ghwebhook.NewServer(ghwebhook.ServerConfig{
		Store:         st,
		WebhookSecret: whSecret,
		RelaySecret:   relaySecret,
	})
	stop := make(chan struct{})
	go srv.PruneEvery(pruneInterval, stop)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      ghwebhook.MaxLongPollWait + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		close(stop)
		_ = httpSrv.Close()
	}()

	fmt.Fprintf(os.Stderr, "github-webhook: listening on %s\n", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
