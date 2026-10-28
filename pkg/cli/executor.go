package cli

// `grain executor` (and alias `grain modelexec`) -- the daemon a remote executor machine runs.
//
// It connects outbound to a Grain installation, registers as an active source
// for the whole deployment, and executes agent CLIs (agy, claude, codex)
// using local ambient credentials while tunneling MCP tool calls back to Grain's sandboxes.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/agent/codex"
	"github.com/bwsalmon/grain/pkg/modelexec"
)

// binaryFlag collects repeated -binary NAME=PATH flags.
type binaryFlag map[string]string

func (b binaryFlag) String() string {
	names := make([]string, 0, len(b))
	for name, path := range b {
		names = append(names, name+"="+path)
	}
	return strings.Join(names, ",")
}

func (b binaryFlag) Set(value string) error {
	name, path, ok := strings.Cut(value, "=")
	name, path = strings.TrimSpace(name), strings.TrimSpace(path)
	if !ok || name == "" || path == "" {
		return fmt.Errorf("want NAME=PATH (for example agy=/my/copy/binary), got %q", value)
	}
	if _, exists := b[name]; exists {
		return fmt.Errorf("%s is named twice", name)
	}
	b[name] = path
	return nil
}

func executorCmd(args []string) {
	fs := flag.NewFlagSet("grain executor", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: grain executor [flags]

Connects this host to a Grain daemon as a remote model executor.
Allows Grain sandboxes to run agent model CLIs (Antigravity, Claude, Codex)
on this machine using its local ambient credentials or environment.

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Bootstrap Script:
  For automated setup, binary download, and CLI discovery, use:
    ./scripts/run-remote-executor.sh --server <host:port> --token-file <path>
  or single-command via curl:
    curl -fsSL https://<grain-host>/api/executors/install.sh | bash -s -- --server <host:port> --token-file <path>
`)
	}
	addr := fs.String("server", "", "address or URL of the grain daemon's executor listener (host:port or https://...). Required")
	serverAlias := fs.String("addr", "", "alias for -server")
	tokenFile := fs.String("token-file", "", "file holding the shared token this daemon authenticates with")
	tokenStr := fs.String("token", "", "shared token string this daemon authenticates with (alternative to -token-file)")
	name := fs.String("name", "", "how this machine identifies itself to grain; defaults to its hostname")
	instanceID := fs.String("instance-id", "", "unique instance ID of this executor; auto-generated if empty")
	idAlias := fs.String("id", "", "alias for -instance-id")
	// Accepted and ignored: an executor belongs to no team since the
	// single-tenant reset (stage 4c), and existing install scripts still
	// pass it.
	_ = fs.String("team", "default", "ignored: an executor serves the whole deployment")
	binaries := binaryFlag{}
	fs.Var(binaries, "binary", "NAME=PATH: local binary that stands in for a CLI grain asks for (-binary agy=/usr/bin/agy). Repeatable. Include grain=<path to this binary>")
	codexAuthFile := fs.String("codex-auth-file", "", "auth.json this machine's own `codex login` wrote")
	concurrency := fs.Int("concurrency", 1, "maximum number of concurrent agents this executor will run")
	grainURL := fs.String("grain-url", "", "how this machine reaches grain's UI/API (http://grain.internal:4242 or https://...)")
	audience := fs.String("audience", "", "audience to use when generating Google ID tokens for Cloud Run / IAP proxy")
	useTLS := fs.Bool("tls", false, "dial the listener over TLS")
	caFile := fs.String("ca-file", "", "PEM file of the certificate authority to verify grain's certificate against. Implies -tls")
	serverName := fs.String("tls-server-name", "", "server name to verify grain's certificate against when it differs from the -server host. Implies -tls")
	iapAudience := fs.String("iap-audience", "", "OAuth client ID audience for GCP Identity-Aware Proxy (IAP) authentication")
	fs.Parse(args)

	targetAddr := *addr
	if targetAddr == "" {
		targetAddr = *serverAlias
	}
	if targetAddr == "" && *grainURL != "" {
		targetAddr = *grainURL
	}

	targetToken := *tokenStr
	if targetToken == "" && *tokenFile != "" {
		t, err := daemon.ReadTokenFile(*tokenFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "grain executor: "+err.Error())
			os.Exit(1)
		}
		targetToken = t
	}
	if targetToken == "" {
		targetToken = os.Getenv("GRAIN_EXECUTOR_TOKEN")
	}
	if targetToken == "" {
		targetToken = os.Getenv("GRAIN_TOKEN")
	}

	targetInstanceID := *instanceID
	if targetInstanceID == "" {
		targetInstanceID = *idAlias
	}

	targetIAPAudience := *iapAudience
	if targetIAPAudience == "" {
		targetIAPAudience = *audience
	}
	if targetIAPAudience == "" {
		targetIAPAudience = os.Getenv("GRAIN_IAP_AUDIENCE")
		if targetIAPAudience == "" {
			targetIAPAudience = os.Getenv("GRAIN_AUDIENCE")
			if targetIAPAudience == "" {
				targetIAPAudience = os.Getenv("IAP_AUDIENCE")
			}
		}
	}

	if err := runExecutor(targetAddr, *tokenFile, targetToken, *name, targetInstanceID, *grainURL, *codexAuthFile, targetIAPAudience, binaries, *concurrency, *useTLS, *caFile, *serverName); err != nil {
		fmt.Fprintln(os.Stderr, "grain executor: "+err.Error())
		os.Exit(1)
	}
}

func runExecutor(addr, tokenFile, tokenStr, name, instanceID, grainURL, codexAuthFile, iapAudience string, binaries binaryFlag, concurrency int, useTLS bool, caFile, serverName string) error {
	if addr == "" {
		return errors.New("-server (or -addr) is required: the address or URL of the grain daemon's executor listener")
	}
	token := tokenStr
	if token == "" && tokenFile != "" {
		var err error
		token, err = daemon.ReadTokenFile(tokenFile)
		if err != nil {
			return err
		}
	}
	if token == "" {
		token = os.Getenv("GRAIN_TOKEN")
	}
	if token == "" {
		return errors.New("-token-file (or -token) is required: the shared token this daemon authenticates with")
	}
	if len(binaries) == 0 {
		return errors.New("at least one -binary NAME=PATH is required")
	}
	if grainURL == "" && (strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://")) {
		grainURL = addr
	}
	d := &modelexec.Daemon{
		Addr:          addr,
		Token:         token,
		Name:          name,
		InstanceID:    instanceID,
		Binaries:      binaries,
		ServerURL:     grainURL,
		CodexAuthFile: codexAuthFile,
		Concurrency:   concurrency,
		IAPAudience:   iapAudience,
		Audience:      iapAudience,
		Logf:          log.Printf,
	}

	if err := d.CheckBinaries(); err != nil {
		return err
	}
	if codexAuthFile != "" {
		if err := codex.ValidateAuthFile(codexAuthFile); err != nil {
			return err
		}
	}
	if useTLS || caFile != "" || serverName != "" {
		cfg, err := executorClientTLS(caFile, serverName)
		if err != nil {
			return err
		}
		d.TLS = cfg
	}
	if iapAudience != "" {
		log.Printf("grain executor: GCP IAP authentication enabled for audience %s", iapAudience)
	}
	if _, ok := binaries["grain"]; !ok {
		log.Print("grain executor: no grain=<path> binary was configured -- " +
			"a run's CLI forks `grain mcpserver` on this machine to reach its sandbox, " +
			"so dispatches here will fail until one is configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if codexAuthFile != "" {
		// This machine's runs share its login as a daemon's do, so it is
		// renewed ahead of the CLI the same way (codex.Refresher).
		go (&codex.Refresher{Files: func() []string { return []string{codexAuthFile} }}).Run(ctx)
	}
	log.Printf("grain executor: offering %s to %s", binaries.String(), addr)
	return d.Run(ctx)
}

func executorClientTLS(caFile, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading -ca-file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("-ca-file %s holds no PEM certificate", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
