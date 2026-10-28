package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"github.com/bwsalmon/granule/pkg/relay"
)

const relayUsage = `usage: granule relay [flags]

Runs granule relay: the in-cluster hop an off-cluster controller uses to
reach sandbox pods' access listeners (see docs/sandbox-relay.md).

The controller dials -controller-listen once over TLS 1.3, authenticated
with the controller token (-token-file or $GRANULE_RELAY_TOKEN) or a client
certificate signed by -client-ca, and opens one stream per sandbox
operation naming its target. The relay resolves the target to a pod IP by
watching pods in -namespace that match -selector (get/list/watch only),
dials <podIP>:-sandbox-port and splices bytes both ways. TLS and the
per-sandbox token run end to end between the controller and the sandbox;
the relay keeps no state and holds no sandbox secrets.

Flags:`

func runRelay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule relay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), relayUsage)
		fs.PrintDefaults()
	}
	listen := fs.String("controller-listen", ":8443", "address controllers connect to (front it with a LoadBalancer Service)")
	certFile := fs.String("tls-cert", "", "PEM server certificate for the controller listener (required)")
	keyFile := fs.String("tls-key", "", "PEM private key for -tls-cert (required)")
	clientCA := fs.String("client-ca", "", "PEM CA bundle; when set, controllers must present a client certificate it signed (mTLS)")
	tokenFile := fs.String("token-file", "", "file holding the controller bearer token (or set $GRANULE_RELAY_TOKEN)")
	namespace := fs.String("namespace", "", "namespace sandbox pods run in (default: the relay's own)")
	selector := fs.String("selector", "granule.gke.io/sandbox=true", "label selector every target pod must match")
	nameLabel := fs.String("name-label", "granule.gke.io/sandbox-name", "label whose value names a sandbox, matched as well as the pod name (empty: pod name only)")
	sandboxPort := fs.Int("sandbox-port", 7443, "TCP port sandbox pods' access servers listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("relay: unexpected arguments %q", fs.Args())
	}
	if *certFile == "" || *keyFile == "" {
		return errors.New("relay: -tls-cert and -tls-key are required")
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		return fmt.Errorf("relay: loading -tls-cert/-tls-key: %w", err)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	if *clientCA != "" {
		pemBytes, err := os.ReadFile(*clientCA)
		if err != nil {
			return fmt.Errorf("relay: reading -client-ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return fmt.Errorf("relay: -client-ca %s holds no certificates", *clientCA)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	token := os.Getenv("GRANULE_RELAY_TOKEN")
	_ = os.Unsetenv("GRANULE_RELAY_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return fmt.Errorf("relay: reading -token-file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}

	resolver, err := relay.InClusterPodResolver(*namespace, *selector, *nameLabel)
	if err != nil {
		return err
	}
	srv, err := relay.New(relay.Config{
		Token:       token,
		TLSConfig:   tlsCfg,
		Resolver:    resolver,
		SandboxPort: *sandboxPort,
		Logf:        log.New(stderr, "", log.LstdFlags).Printf,
	})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		if err := resolver.Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "granule relay: pod watch stopped: %v\n", err)
			cancel()
		}
	}()
	fmt.Fprintf(stdout, "granule relay: listening for controllers on %s, routing to pods matching %q on port %d\n", ln.Addr(), *selector, *sandboxPort)
	return srv.Serve(ctx, ln)
}
