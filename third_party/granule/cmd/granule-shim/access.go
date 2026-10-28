package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/netshim"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

// The sandbox access API's configuration (docs/sandbox-access-api.md,
// section 6). With neither GRANULE_ACCESS_LISTEN nor GRANULE_ACCESS_DIAL
// set the access server does not start and "granule run" behaves as it
// always has.
const (
	envAccessListen        = "GRANULE_ACCESS_LISTEN"
	envAccessDial          = "GRANULE_ACCESS_DIAL"
	envAccessSandboxID     = "GRANULE_ACCESS_SANDBOX_ID"
	envAccessToken         = "GRANULE_ACCESS_TOKEN"
	envAccessTokenFile     = "GRANULE_ACCESS_TOKEN_FILE"
	envAccessTLSCertFile   = "GRANULE_ACCESS_TLS_CERT_FILE"
	envAccessTLSKeyFile    = "GRANULE_ACCESS_TLS_KEY_FILE"
	envAccessCAFile        = "GRANULE_ACCESS_CA_FILE"
	envAccessCertFile      = "GRANULE_ACCESS_CERT_FILE"
	envAccessKeyFile       = "GRANULE_ACCESS_KEY_FILE"
	envAccessControllerIPs = "GRANULE_ACCESS_CONTROLLER_IPS"
	envNetshimControllers  = "NETSHIM_CONTROLLER_IPS"
	envNetshimAccessPorts  = "NETSHIM_ACCESS_PORTS"
)

// accessConfig is the access server's configuration as read from the
// environment.
type accessConfig struct {
	Listen    string
	Dial      string
	SandboxID string
	Token     string

	// TLSCertFile and TLSKeyFile are the server certificate a tcp://
	// listener serves.
	TLSCertFile, TLSKeyFile string

	// CAFile, CertFile and KeyFile verify the controller and present a
	// client certificate in dial-out mode.
	CAFile, CertFile, KeyFile string

	// ControllerIPs are the addresses of whatever dials this sandbox's
	// listener (granule relay pods, an in-cluster controller) or that it
	// dials out to, which netshim keeps away from the guest.
	ControllerIPs []net.IP
}

func (a accessConfig) enabled() bool { return a.Listen != "" || a.Dial != "" }

// accessFromEnv reads the access configuration and scrubs the token from
// this process before anything else can inherit it: GRANULE_ACCESS_TOKEN is
// unset, and GRANULE_ACCESS_TOKEN_FILE is unlinked once read (a read-only
// mount, such as a Secret volume, is left in place with a warning: it is
// still never visible to the guest, which sees none of this container's
// filesystem). It runs before cloud-hypervisor or any other child starts.
func accessFromEnv() (accessConfig, error) {
	a := accessConfig{
		Listen:      strings.TrimSpace(os.Getenv(envAccessListen)),
		Dial:        strings.TrimSpace(os.Getenv(envAccessDial)),
		SandboxID:   strings.TrimSpace(os.Getenv(envAccessSandboxID)),
		Token:       strings.TrimSpace(os.Getenv(envAccessToken)),
		TLSCertFile: os.Getenv(envAccessTLSCertFile),
		TLSKeyFile:  os.Getenv(envAccessTLSKeyFile),
		CAFile:      os.Getenv(envAccessCAFile),
		CertFile:    os.Getenv(envAccessCertFile),
		KeyFile:     os.Getenv(envAccessKeyFile),
	}
	_ = os.Unsetenv(envAccessToken)
	if path := os.Getenv(envAccessTokenFile); path != "" {
		_ = os.Unsetenv(envAccessTokenFile)
		b, err := os.ReadFile(path)
		if err != nil {
			return accessConfig{}, fmt.Errorf("%s: %w", envAccessTokenFile, err)
		}
		a.Token = strings.TrimSpace(string(b))
		if err := os.Remove(path); err != nil {
			log.Printf("warning: could not unlink %s after reading the access token (%v); it stays in this container's filesystem, which the guest cannot see", envAccessTokenFile, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envAccessControllerIPs)); raw != "" {
		ips, err := netshim.ParseControllerIPs(raw)
		if err != nil {
			return accessConfig{}, fmt.Errorf("%s: %w", envAccessControllerIPs, err)
		}
		a.ControllerIPs = ips
	}
	if !a.enabled() {
		return a, nil
	}
	if a.Listen != "" && strings.HasPrefix(a.Listen, "tcp://") {
		if a.Token == "" {
			return accessConfig{}, fmt.Errorf("%s=%s needs a per-sandbox token (%s or %s): a network listener never runs unauthenticated", envAccessListen, a.Listen, envAccessTokenFile, envAccessToken)
		}
		if a.TLSCertFile == "" || a.TLSKeyFile == "" {
			return accessConfig{}, fmt.Errorf("%s=%s needs %s and %s: a network listener always serves TLS", envAccessListen, a.Listen, envAccessTLSCertFile, envAccessTLSKeyFile)
		}
	}
	if a.Dial != "" {
		if a.SandboxID == "" {
			return accessConfig{}, fmt.Errorf("%s needs %s", envAccessDial, envAccessSandboxID)
		}
		if a.Token == "" {
			return accessConfig{}, fmt.Errorf("%s needs a per-sandbox token (%s or %s)", envAccessDial, envAccessTokenFile, envAccessToken)
		}
		// A controller named by a literal IP is guarded without being
		// listed separately; a hostname has to be listed in
		// GRANULE_ACCESS_CONTROLLER_IPS, since netshim filters addresses.
		if ip := dialHostIP(a.Dial); ip != nil {
			a.ControllerIPs = append(a.ControllerIPs, ip)
		}
	}
	return a, nil
}

// dialHostIP is the controller's address in a GRANULE_ACCESS_DIAL value
// when it is a literal IP, or nil.
func dialHostIP(dial string) net.IP {
	hostPort := dial
	if strings.Contains(dial, "://") {
		u, err := url.Parse(dial)
		if err != nil {
			return nil
		}
		hostPort = u.Host
	}
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// listenPort is the TCP port of a tcp:// GRANULE_ACCESS_LISTEN, or 0.
func (a accessConfig) listenPort() uint16 {
	if !strings.HasPrefix(a.Listen, "tcp://") {
		return 0
	}
	_, port, err := net.SplitHostPort(strings.TrimPrefix(a.Listen, "tcp://"))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}

// exportNetshimAccess tells netshim about the access server, for inline
// networking, through the variables netshim.FromEnv reads: a tcp://
// listener's port goes in NETSHIM_ACCESS_PORTS, so connections to it from
// any pod (granule relay, an in-cluster controller) reach this process
// rather than the guest, and a's controller addresses go in
// NETSHIM_CONTROLLER_IPS, so the guest cannot reach them. (In external
// mode the netshim init container reads its own environment; set the same
// variables there.)
func exportNetshimAccess(a accessConfig) error {
	if port := a.listenPort(); port != 0 {
		ports := strings.TrimSpace(os.Getenv(envNetshimAccessPorts))
		p := strconv.Itoa(int(port))
		if !slices.Contains(strings.Split(ports, ","), p) {
			if ports != "" {
				ports += ","
			}
			if err := os.Setenv(envNetshimAccessPorts, ports+p); err != nil {
				return err
			}
		}
	}
	if len(a.ControllerIPs) == 0 {
		return nil
	}
	var all []net.IP
	if raw := strings.TrimSpace(os.Getenv(envNetshimControllers)); raw != "" {
		ips, err := netshim.ParseControllerIPs(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", envNetshimControllers, err)
		}
		all = ips
	}
	seen := map[string]bool{}
	for _, ip := range all {
		seen[ip.String()] = true
	}
	for _, ip := range a.ControllerIPs {
		if !seen[ip.String()] {
			seen[ip.String()] = true
			all = append(all, ip)
		}
	}
	strs := make([]string, len(all))
	for i, ip := range all {
		strs[i] = ip.String()
	}
	return os.Setenv(envNetshimControllers, strings.Join(strs, ","))
}

// accessServer builds the pod-side access server for a, reaching the guest
// agent over cfg's vsock device.
func accessServer(a accessConfig, cfg config.Config) (*sandbox.Server, error) {
	if cfg.VsockSocket == "" {
		return nil, errors.New("the sandbox access API needs the guest's vsock device, and CHV_VSOCK_SOCKET is empty")
	}
	gcfg, err := guestexec.FromEnv()
	if err != nil {
		return nil, err
	}
	gcfg.Socket = cfg.VsockSocket
	scfg := sandbox.ServerConfig{
		SandboxID:      a.SandboxID,
		Token:          a.Token,
		WrapperVersion: "granule-shim",
		Guest:          gcfg,
	}
	if a.TLSCertFile != "" || a.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(a.TLSCertFile, a.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading %s/%s: %w", envAccessTLSCertFile, envAccessTLSKeyFile, err)
		}
		scfg.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}
	return sandbox.NewServer(scfg), nil
}

// startAccess starts serving the access API in the background for as long
// as ctx lives. It binds the listener before returning, so a port that is
// taken fails the boot rather than a later caller.
func startAccess(ctx context.Context, a accessConfig, cfg config.Config) error {
	if !a.enabled() {
		return nil
	}
	srv, err := accessServer(a, cfg)
	if err != nil {
		return err
	}
	if a.Listen != "" {
		ln, err := sandbox.Listen(a.Listen)
		if err != nil {
			return fmt.Errorf("%s: %w", envAccessListen, err)
		}
		log.Printf("serving the sandbox access API on %s", ln.Addr())
		go func() {
			if err := srv.Serve(ctx, ln); err != nil {
				log.Printf("sandbox access listener stopped: %v", err)
			}
		}()
	}
	if a.Dial != "" {
		opts := sandbox.DialOutOptions{CAFile: a.CAFile, CertFile: a.CertFile, KeyFile: a.KeyFile}
		log.Printf("dialing out to the sandbox access controller at %s", a.Dial)
		go func() {
			if err := srv.DialOut(ctx, a.Dial, opts); err != nil && ctx.Err() == nil {
				log.Printf("sandbox access dial-out stopped: %v", err)
			}
		}()
	}
	return nil
}
