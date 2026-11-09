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

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/version"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// Main is the entrypoint called by `cmd/grain-registry` (and any wrapper
// edition binaries after registering their extension providers).
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "grain-registry: %v\n", err)
		os.Exit(1)
	}
}

// Run executes the `grain-registry` CLI (`version`, `validate`, or `serve`).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-version":
			return runVersion(args[1:], stdout, stderr)
		case "validate":
			return runValidate(ctx, args[1:], stdout, stderr)
		case "serve":
			return runServe(ctx, args[1:], stdout, stderr)
		case "demo":
			return runDemo(ctx, args[1:], stdout, stderr)
		case "help", "--help", "-h":
			printUsage(stdout)
			return nil
		}
	}
	return runServe(ctx, args, stdout, stderr)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  grain-registry [serve] -config <path>")
	fmt.Fprintln(w, "  grain-registry validate -config <path>")
	fmt.Fprintln(w, "  grain-registry version [--json]")
}

type versionJSON struct {
	Release     string `json:"release,omitempty"`
	Edition     string `json:"edition,omitempty"`
	Revision    string `json:"revision,omitempty"`
	CommittedAt string `json:"committedAt,omitempty"`
	Modified    bool   `json:"modified,omitempty"`
	Unversioned bool   `json:"unversioned,omitempty"`
}

func runVersion(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grain-registry version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output version information as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info := version.Get()
	if *jsonOut {
		out := versionJSON{
			Release:     info.Release,
			Edition:     info.Edition,
			Revision:    info.Revision,
			Modified:    info.Modified,
			Unversioned: info.Unversioned,
		}
		if !info.Time.IsZero() {
			out.CommittedAt = info.Time.UTC().Format(time.RFC3339)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	ver := info.Release
	if ver == "" {
		ver = "dev"
	}
	rev := info.Revision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" {
		fmt.Fprintf(stdout, "grain-registry %s (%s)\n", ver, rev)
	} else {
		fmt.Fprintf(stdout, "grain-registry %s\n", ver)
	}
	return nil
}

func runValidate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grain-registry validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the hub YAML config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := strings.TrimSpace(*cfgPath)
	if path == "" && fs.NArg() == 1 {
		path = strings.TrimSpace(fs.Arg(0))
	}
	if path == "" {
		return errors.New("validate requires -config <path>")
	}
	rawBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config %s: %w", path, err)
	}
	cfg, err := parseRawConfig(rawBytes)
	if err != nil {
		return err
	}
	var resolver GroupResolver
	if cfg.Groups.Provider == "static" {
		resolver, _ = BuildGroupResolver(ctx, cfg.Groups)
	}
	if err := cfg.ValidateWithResolver(resolver); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "config %s is valid\n", path)
	return nil
}

func parseRawConfig(rawBytes []byte) (*Config, error) {
	return ParseConfigWithResolver(rawBytes, nil)
}

// ParseConfigWithResolver unmarshals YAML bytes, applies defaults, and validates
// with an optional GroupResolver / GroupNormalizer (Core follow-up C-1).
func ParseConfigWithResolver(data []byte, resolver any) (*Config, error) {
	expanded := os.ExpandEnv(string(data))
	var cfg Config
	dec := newStrictYAMLDecoder(expanded)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing hub config YAML: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.ValidateWithResolver(resolver); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func newStrictYAMLDecoder(expanded string) interface{ Decode(any) error } {
	return yamlStrictDecoder(expanded)
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grain-registry", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", os.Getenv("GRAIN_REGISTRY_CONFIG"), "path to the hub YAML config file")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		return runVersion(nil, stdout, stderr)
	}
	if strings.TrimSpace(*cfgPath) == "" {
		return errors.New("-config <path> (or GRAIN_REGISTRY_CONFIG) is required")
	}

	rawBytes, err := os.ReadFile(*cfgPath)
	if err != nil {
		return fmt.Errorf("reading config %s: %w", *cfgPath, err)
	}
	var cfg Config
	if err := yamlStrictDecoder(os.ExpandEnv(string(rawBytes))).Decode(&cfg); err != nil {
		return fmt.Errorf("parsing hub config YAML: %w", err)
	}
	cfg.ApplyDefaults()

	groups, err := BuildGroupResolver(ctx, cfg.Groups)
	if err != nil {
		return err
	}
	if err := cfg.ValidateWithResolver(groups); err != nil {
		return err
	}

	frontIdentity, err := BuildFrontIdentity(ctx, cfg.Auth)
	if err != nil {
		return err
	}
	delegation, err := BuildDelegationPolicy(ctx, cfg.Auth)
	if err != nil {
		return err
	}
	signer, err := BuildSigner(ctx, cfg.Signing)
	if err != nil {
		return err
	}
	transport, err := BuildConnectTransport(cfg.Tunnel)
	if err != nil {
		return err
	}

	var compute Compute
	var resources InstanceResources
	if strings.TrimSpace(cfg.Compute.Provider) != "none" {
		compute, err = BuildCompute(ctx, cfg.Compute)
		if err != nil {
			return err
		}
		resources, err = BuildInstanceResources(ctx, cfg)
		if err != nil {
			return err
		}
	}
	var configRepos ConfigRepos
	if strings.TrimSpace(cfg.ConfigRepos.Provider) != "" && strings.TrimSpace(cfg.ConfigRepos.Provider) != "none" {
		configRepos, err = BuildConfigRepos(ctx, cfg.ConfigRepos)
		if err != nil {
			return err
		}
	}

	var st *store.Store
	var isDisabled webauth.DisabledChecker
	if strings.TrimSpace(cfg.Database.URL) != "" {
		st, err = store.Open(ctx, cfg.Database.URL, store.Thresholds{
			DegradedAfter:     cfg.Catalog.DegradedAfter,
			OfflineAfter:      cfg.Catalog.OfflineAfter,
			ReplicaTTL:        cfg.Tunnel.ReplicaTTL,
			DefaultUnitsQuota: cfg.Quotas.DefaultInstanceUnits,
			DefaultConnQuota:  cfg.Quotas.DefaultConnected,
			ReservationPeriod: cfg.Quotas.ReservationPeriod,
			DBRetentionPeriod: cfg.InstanceState.DBRetentionPeriod,
		})
		if err != nil {
			return err
		}
		defer st.Close()

		if err := seedStoreFromConfig(ctx, st, cfg); err != nil {
			return err
		}
		isDisabled = st.IsOwnerDisabled
		if sa, ok := delegation.(interface{ SetStore(*store.Store) }); ok {
			sa.SetStore(st)
		}
		if resStore, ok := resources.(interface{ SetStore(*store.Store) }); ok {
			resStore.SetStore(st)
		}
	}

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:                frontIdentity,
		Groups:                  groups,
		AllowedDomains:          cfg.Auth.AllowedDomains,
		Admins:                  cfg.Auth.Admins,
		Creators:                cfg.Auth.Creators,
		ServicePrincipals:       cfg.Auth.ServicePrincipals,
		ServicePrincipalChecker: delegation.IsServicePrincipalReader,
		IsDisabled:              isDisabled,
	})
	if err != nil {
		return err
	}

	replicaID := "replica-" + uuid.NewString()[:8]
	peerAddr := cfg.Tunnel.SelfAddr
	if peerAddr == "" {
		peerAddr = cfg.Tunnel.PeerListen
	}

	srv, err := NewServer(ServerOptions{
		Config:      cfg,
		Store:       st,
		Admitter:    admitter,
		Groups:      groups,
		Delegation:  delegation,
		Signer:      signer,
		Transport:   transport,
		Compute:     compute,
		Resources:   resources,
		ConfigRepos: configRepos,
		Metrics:     NewMetrics(),
		ReplicaID:   replicaID,
		PeerAddr:    peerAddr,
	})
	if err != nil {
		return err
	}

	var rec BackgroundReconciler
	if st != nil {
		var hostCache ReconcilerHostCache
		if srv.Router() != nil {
			hostCache = srv.Router().HostCache()
		}
		rec, err = BuildReconciler(ReconcilerBuildOptions{
			Config:      cfg,
			Store:       st,
			Compute:     compute,
			Resources:   resources,
			ConfigRepos: configRepos,
			Signer:      signer,
			HostCache:   hostCache,
			HolderID:    replicaID,
		})
		if err != nil {
			return err
		}
		if rec != nil {
			srv.SetOrphanProvider(rec)
		}
	}

	mainLn, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Listen, err)
	}
	defer mainLn.Close()

	var jwksLn net.Listener
	if cfg.JWKSListen != "" && cfg.JWKSListen != cfg.Listen {
		jwksLn, err = net.Listen("tcp", cfg.JWKSListen)
		if err != nil {
			return fmt.Errorf("listening on JWKS address %s: %w", cfg.JWKSListen, err)
		}
		defer jwksLn.Close()
	}

	var peerLn net.Listener
	if cfg.Tunnel.PeerListen != "" && cfg.Tunnel.PeerListen != cfg.Listen && cfg.Tunnel.PeerListen != cfg.JWKSListen {
		peerLn, err = net.Listen("tcp", cfg.Tunnel.PeerListen)
		if err != nil {
			return fmt.Errorf("listening on peer address %s: %w", cfg.Tunnel.PeerListen, err)
		}
		defer peerLn.Close()
		if cfg.Tunnel.SelfAddr == "" {
			srv.Router().SetPeerAddr(peerLn.Addr().String())
		}
	}

	srv.StartBackground(ctx)
	if rec != nil {
		rec.Start(ctx)
	}

	mainHTTPSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	var jwksHTTPSrv *http.Server
	if jwksLn != nil {
		jwksHTTPSrv = &http.Server{
			Handler:           srv.PublicHandler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
	}
	var peerHTTPSrv *http.Server
	if peerLn != nil {
		peerHTTPSrv = &http.Server{
			Handler:           srv.PeerHandler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
	}

	errCh := make(chan error, 3)
	go func() {
		if err := mainHTTPSrv.Serve(mainLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("main server: %w", err)
		}
	}()
	if jwksHTTPSrv != nil {
		go func() {
			if err := jwksHTTPSrv.Serve(jwksLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("jwks server: %w", err)
			}
		}()
	}
	if peerHTTPSrv != nil {
		go func() {
			if err := peerHTTPSrv.Serve(peerLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("peer server: %w", err)
			}
		}()
	}

	fmt.Fprintf(stdout, "grain-registry listening on %s (jwks: %s, peer: %s)\n", mainLn.Addr().String(), cfg.JWKSListen, cfg.Tunnel.PeerListen)

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.BeginDrain(shutCtx)
		_ = mainHTTPSrv.Shutdown(shutCtx)
		if jwksHTTPSrv != nil {
			_ = jwksHTTPSrv.Shutdown(shutCtx)
		}
		if peerHTTPSrv != nil {
			_ = peerHTTPSrv.Shutdown(shutCtx)
		}
		if st != nil {
			_ = st.DeleteReplica(shutCtx, replicaID)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func seedStoreFromConfig(ctx context.Context, st *store.Store, cfg Config) error {
	if len(cfg.Compute.Shapes) > 0 {
		rows := make([]store.ShapeRow, 0, len(cfg.Compute.Shapes))
		for _, sh := range cfg.Compute.Shapes {
			rows = append(rows, store.ShapeRow{
				Name:         sh.Name,
				MachineType:  sh.MachineType,
				BootDiskGB:   sh.BootDiskGB,
				BootDiskIOPS: sh.BootDiskIOPS,
				QuotaUnits:   sh.QuotaUnits,
				Description:  sh.Description,
			})
		}
		if err := st.SeedShapes(ctx, rows); err != nil {
			return err
		}
	}
	if len(cfg.InstanceSQL.SQLInstances) > 0 {
		rows := make([]store.SQLInstanceRow, 0, len(cfg.InstanceSQL.SQLInstances))
		for _, sq := range cfg.InstanceSQL.SQLInstances {
			rows = append(rows, store.SQLInstanceRow{
				Name:           sq.Name,
				ConnectionName: sq.ConnectionName,
				PrivateIP:      sq.PrivateIP,
				MaxDatabases:   sq.MaxDatabases,
			})
		}
		if err := st.SeedSQLInstances(ctx, rows); err != nil {
			return err
		}
	}
	for _, ov := range cfg.Quotas.Overrides {
		if strings.HasPrefix(ov.Principal, "user:") {
			email := strings.TrimPrefix(ov.Principal, "user:")
			if err := st.SetQuota(ctx, email, ov.MaxInstanceUnits, ov.MaxConnected, "config"); err != nil {
				return err
			}
		}
	}
	return nil
}
