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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/grit"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grit serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:4244", "HTTP listen address")
	dataDir := fs.String("data-dir", "./grit-data", "state and SQLite directory")
	executor := fs.String("executor", "local", "job executor backend (local)")
	concurrency := fs.Int("concurrency", 8, "maximum concurrent jobs")
	signingKey := fs.String("signing-key", "", "ECDSA P-256 PEM private key path (defaults to <data-dir>/attest.key)")
	pkgCacheDir := fs.String("pkg-cache-dir", "", "package cache directory (defaults to <data-dir>/pkgcache)")
	pkgCacheMaxBytes := fs.Int64("pkg-cache-max-bytes", 10<<30, "package cache maximum bytes")
	grainURL := fs.String("grain-url", "", "optional grain base URL for reverse-tunnel /v1/session mode")
	clientToken := fs.String("client-token", "", "client token (<id>.<secret>) for -grain-url reverse-tunnel mode")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *executor != "local" {
		fmt.Fprintf(stderr, "grit serve: unsupported executor %q (want local)\n", *executor)
		return 2
	}

	keyPath := *signingKey
	if keyPath == "" {
		keyPath = filepath.Join(*dataDir, "attest.key")
	}
	signer, err := attest.LoadOrCreateFileSigner(keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "grit serve: signing key: %v\n", err)
		return 1
	}

	cacheDir := *pkgCacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(*dataDir, "pkgcache")
	}
	pkgAdmin, err := pkgcache.OpenAdmin(cacheDir, *pkgCacheMaxBytes)
	if err != nil {
		fmt.Fprintf(stderr, "grit serve: package cache: %v\n", err)
		return 1
	}

	loc, err := grit.NewLocal(grit.LocalConfig{
		DataDir:       *dataDir,
		Executor:      *executor,
		Concurrency:   *concurrency,
		Signer:        signer,
		PkgCacheAdmin: pkgAdmin,
	})
	if err != nil {
		fmt.Fprintf(stderr, "grit serve: %v\n", err)
		return 1
	}
	defer func() { _ = loc.Close() }()

	srv := grit.NewServer(grit.ServerConfig{Local: loc})

	if *grainURL != "" {
		go dialGrainLoop(ctx, *grainURL, func(ctx context.Context) error {
			return grit.DialSession(ctx, grit.SessionDialConfig{
				GrainURL:    *grainURL,
				ClientToken: *clientToken,
				Server:      srv,
			})
		}, stderr, 100*time.Millisecond, 30*time.Second)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "grit serve: listen %s: %v\n", *listen, err)
		return 1
	}
	defer func() { _ = ln.Close() }()

	fmt.Fprintf(stdout, "grit serve listening on %s (epoch %s, key %s)\n", ln.Addr().String(), loc.Store().Epoch(), signer.KeyID())

	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 15 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return 0
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "grit serve: %v\n", err)
			return 1
		}
		return 0
	}
}

// dialGrainLoop keeps a reverse-tunnel /v1/session to grain open for as long
// as ctx lives. It used to redial every 100ms in silence, so a grain that
// refused the session (a wrong token, or one that does not serve /v1/session
// at all) left grit dialing ten times a second with nothing in its log to
// say why no CI arrived (docs/test-runs/2026-10-29-grit-backend-ci-shutdown.md,
// F5). A failure is now logged when it first happens and whenever it
// changes, and the redial backs off from minWait to maxWait; a session that
// was up resets it.
func dialGrainLoop(ctx context.Context, grainURL string, dial func(context.Context) error, stderr io.Writer, minWait, maxWait time.Duration) {
	wait := minWait
	lastErr := ""
	for ctx.Err() == nil {
		err := dial(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if lastErr != "" {
				fmt.Fprintf(stderr, "grit serve: reverse session to %s closed; redialing\n", grainURL)
			}
			lastErr = ""
			wait = minWait
		} else {
			if msg := err.Error(); msg != lastErr {
				// One line, however much HTML a refusing proxy sent back.
				short := strings.Join(strings.Fields(msg), " ")
				if len(short) > 200 {
					short = short[:200] + "..."
				}
				fmt.Fprintf(stderr, "grit serve: reverse session to %s: %s (retrying, backing off to %s)\n", grainURL, short, maxWait)
				lastErr = msg
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err != nil {
			wait *= 2
			if wait > maxWait {
				wait = maxWait
			}
		}
	}
}
