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
	"time"

	"github.com/bwsalmon/grain/pkg/attest"
	"github.com/bwsalmon/grain/pkg/grit"
	"github.com/bwsalmon/grain/pkg/pkgcache"
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

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "grit serve: listen %s: %v\n", *listen, err)
		return 1
	}
	defer func() { _ = ln.Close() }()

	fmt.Fprintf(stdout, "grit serve listening on %s (epoch %s, key %s)\n", ln.Addr().String(), loc.Store().Epoch(), signer.KeyID())

	httpSrv := &http.Server{
		Handler:           grit.NewServer(grit.ServerConfig{Local: loc}),
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
