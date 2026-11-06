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

package agentd

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"net"
	"net/url"
	"sync"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// The transport seam: how grain-agentd reaches a daemon it cannot reach
// the two ways core knows -- directly, or through IAP with -iap-audience
// (hostd.Dialer). A build registers a Transport from init() for a daemon
// behind a proxy of its own (a corporate access proxy, a tunnel), and
// grain-agentd uses it for every -server URL it matches. Core registers
// none.

// DialConfig is what a transport is told about the daemon it is to reach
// and the hostd reaching it.
type DialConfig struct {
	// Server is -server, parsed: an http or https URL with a host.
	Server *url.URL
	// Token is this hostd's token, which names it to the daemon
	// (hostd.TokenHeader); Name the registry name it expects to be, if
	// any; Host this machine's hostname; Build this binary's build.
	Token, Name, Host, Build string
	// TokenFunc, when set, returns the latest silently renewed credential.
	TokenFunc func() string
	// PrivateKey, when set, signs each upgrade request with this host's
	// Ed25519 private key.
	PrivateKey ed25519.PrivateKey
	// IAPAudience is -iap-audience, which a transport may ignore.
	IAPAudience string
	// StateDir is grain-agentd's own state directory (-state-dir); a
	// transport keeping state of its own keeps it in a subdirectory
	// named for itself. Home is the user's home directory.
	StateDir, Home string
}

// InstallConfig is what a transport is told at install time.
type InstallConfig struct {
	// Server is -server, parsed.
	Server *url.URL
	// Home is the user's home directory, StateDir the state directory
	// the installed unit runs with.
	Home, StateDir string
}

// Transport is one way of reaching a daemon.
type Transport struct {
	// Name says which transport this is, in errors.
	Name string
	// Match reports whether this transport is how to reach server. The
	// first registered transport that matches wins; a URL none matches
	// is dialed directly, or through IAP with -iap-audience.
	Match func(server *url.URL) bool
	// Flags, when set, registers the transport's own flags on both
	// `grain-agentd` and `grain-agentd install`, so Dial and Prepare can
	// read them. They must not collide with grain-agentd's own.
	Flags func(fs *flag.FlagSet)
	// Dial returns how a running grain-agentd opens one connection to
	// the daemon -- a stream carrying hostd's session, as hostd.Dialer's
	// Dial returns -- and a func that releases whatever the transport
	// started for it (a helper process, say), called once at exit. It is
	// called once per process, before hostd starts; the returned func is
	// called again for every reconnect.
	Dial func(ctx context.Context, cfg DialConfig) (dial func(context.Context) (net.Conn, error), closeFn func(), err error)
	// Prepare, when set, runs at install time, so slow one-off work (a
	// helper to fetch or build) is done before the first start rather
	// than during it. unitArgs are flags (this transport's own, from
	// Flags) the installed unit's command line carries.
	Prepare func(ctx context.Context, cfg InstallConfig) (unitArgs []string, err error)
}

var (
	transportsMu sync.RWMutex
	transports   []*Transport
)

// RegisterTransport adds t, tried in registration order, until the
// returned func is called. It panics on a transport with no Name, Match
// or Dial.
func RegisterTransport(t Transport) (unregister func()) {
	if t.Name == "" || t.Match == nil || t.Dial == nil {
		panic(fmt.Sprintf("agentd: RegisterTransport(%q): a transport needs a Name, Match and Dial", t.Name))
	}
	p := &t
	transportsMu.Lock()
	transports = append(transports, p)
	transportsMu.Unlock()
	return func() {
		transportsMu.Lock()
		defer transportsMu.Unlock()
		for i, q := range transports {
			if q == p {
				transports = append(transports[:i:i], transports[i+1:]...)
				return
			}
		}
	}
}

func registeredTransports() []*Transport {
	transportsMu.RLock()
	defer transportsMu.RUnlock()
	return append([]*Transport(nil), transports...)
}

// transportFor is the registered transport that matches server, or nil.
func transportFor(server *url.URL) *Transport {
	for _, t := range registeredTransports() {
		if t.Match(server) {
			return t
		}
	}
	return nil
}

func registerTransportFlags(fs *flag.FlagSet) {
	for _, t := range registeredTransports() {
		if t.Flags != nil {
			t.Flags(fs)
		}
	}
}

// dialerFor is how a running grain-agentd reaches cfg.Server: through the
// transport that matches it, else directly (hostd.Dialer), through IAP
// when cfg.IAPAudience is set.
func dialerFor(ctx context.Context, cfg DialConfig) (func(context.Context) (net.Conn, error), func(), error) {
	if t := transportFor(cfg.Server); t != nil {
		dial, closeFn, err := t.Dial(ctx, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		if closeFn == nil {
			closeFn = func() {}
		}
		return dial, closeFn, nil
	}
	d := &hostd.Dialer{
		Server:     cfg.Server.String(),
		Token:      cfg.Token,
		TokenFunc:  cfg.TokenFunc,
		PrivateKey: cfg.PrivateKey,
		Name:       cfg.Name,
		Host:       cfg.Host,
		Build:      cfg.Build,
	}
	if cfg.IAPAudience != "" {
		d.IDToken = hostd.NewIAPTokenProvider(cfg.IAPAudience).Token
	}
	return d.Dial, func() {}, nil
}
