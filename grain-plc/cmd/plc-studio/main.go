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

// Command plc-studio serves the PLC Studio web interface and JSON API backed by
// the Grain Registry and Grain daemon APIs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/grain-plc/pkg/grainclient"
	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
	"github.com/ai-on-gke/grain/grain-plc/pkg/studio"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "plc-studio: %v\n", err)
		os.Exit(1)
	}
}

// Run parses flags and starts the PLC Studio HTTP server until ctx is cancelled.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plc-studio", flag.ContinueOnError)
	fs.SetOutput(stderr)

	listenAddr := fs.String("listen", envOrDefault("PLC_STUDIO_LISTEN", ":4285"), "HTTP listen address")
	registryURL := fs.String("registry-url", os.Getenv("GRAIN_REGISTRY_URL"), "Grain Registry base URL")
	grainURL := fs.String("grain-url", os.Getenv("GRAIN_URL"), "Default Grain daemon base URL")
	grainToken := fs.String("grain-token", os.Getenv("GRAIN_TOKEN"), "Bearer token sent to the -grain-url daemon only (an access code or an external-agent token; $GRAIN_TOKEN)")
	requireAuth := fs.Bool("require-auth", false, "Require verified caller identity on /api/studio/* endpoints")
	defaultUser := fs.String("default-user", os.Getenv("PLC_STUDIO_DEFAULT_USER"), "Fallback caller email when running locally without an auth proxy")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *defaultUser != "" {
		plcseams.RegisterIdentityVerifier(&plcseams.HeaderIdentityVerifier{
			DefaultEmail: *defaultUser,
		})
	}

	recorder := perf.Default()
	client := grainclient.New(grainclient.Config{
		RegistryURL: *registryURL,
		GrainURL:    *grainURL,
		GrainToken:  *grainToken,
		ClientName:  grainclient.ClientPLCStudio,
		Perf:        recorder,
	})
	srv := studio.NewServer(studio.Config{
		Client:      client,
		Perf:        recorder,
		RequireAuth: *requireAuth,
	})

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *listenAddr, err)
	}
	fmt.Fprintf(stdout, "plc-studio listening on http://%s\n", ln.Addr().String())

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
