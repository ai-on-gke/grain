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

package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The daemon's API on a unix socket (docs/access-code.md "Who the code is
// recorded as"). The access code names nobody: a host CLI sending it is
// recorded as access-code:<whatever it calls itself>. A connection to the
// socket instead carries its peer's uid, which the kernel vouches for
// (SO_PEERCRED), and HostAccounts maps that uid to a person, so the
// request is recorded as them with model.SourceHostAccount.
//
// Nothing on the socket is trusted for being there: an unmapped uid (or
// one whose credentials cannot be read) is refused before Server sees
// the request, and a mapped one is admitted only as its person, through
// the allowlist like any verified identity. Every credential a request
// carries -- the access code, a bearer token, a session cookie, a proxy's
// identity headers -- is dropped, and its RemoteAddr is never empty, so
// it can never pass for an in-process caller (singleTenantTrusted).

// HostAccountsEnv is the environment variable fallback for -host-accounts.
const HostAccountsEnv = "GRAIN_HOST_ACCOUNTS"

// APISocketEnv names the socket the host CLI dials instead of a loopback
// -server, overriding the one under $GRAIN_DATA_DIR; "none" turns it off.
const APISocketEnv = "GRAIN_API_SOCKET"

// APISocketPath is where a daemon started with dataDir serves its API on
// a unix socket: in a directory of its own, mode 0700.
func APISocketPath(dataDir string) string {
	return filepath.Join(dataDir, "api", "grain.sock")
}

// HostAccounts maps a host account's uid to the person its requests on
// the API socket are recorded as.
type HostAccounts map[uint32]string

// ParseHostAccounts reads -host-accounts: comma-separated uid=email
// pairs ("1000=alice@example.com,1001=bob@example.com"). Each email is a
// person's, held to what -deployment-owner accepts.
func ParseHostAccounts(raw string) (HostAccounts, error) {
	out := HostAccounts{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		uidStr, email, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("-host-accounts %q: want uid=email", part)
		}
		uid, err := strconv.ParseUint(strings.TrimSpace(uidStr), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("-host-accounts %q: %q is not a uid", part, strings.TrimSpace(uidStr))
		}
		person, err := NormalizeDeploymentOwner(email)
		if err != nil || person == "" {
			return nil, fmt.Errorf("-host-accounts %q: %q is not a person's email address", part, strings.TrimSpace(email))
		}
		if prior, dup := out[uint32(uid)]; dup && prior != person {
			return nil, fmt.Errorf("-host-accounts: uid %d is mapped to both %s and %s", uid, prior, person)
		}
		out[uint32(uid)] = person
	}
	return out, nil
}

// WithDaemonAccount is accounts with the daemon's own uid mapped to owner
// (-deployment-owner) unless accounts already maps it. owner "" leaves
// accounts as they are.
func (accounts HostAccounts) WithDaemonAccount(uid uint32, owner string) HostAccounts {
	out := HostAccounts{}
	for k, v := range accounts {
		out[k] = v
	}
	if owner = strings.TrimSpace(owner); owner != "" {
		if _, ok := out[uid]; !ok {
			out[uid] = owner
		}
	}
	return out
}

// String is accounts as -host-accounts spells them, by uid.
func (accounts HostAccounts) String() string {
	uids := make([]int, 0, len(accounts))
	for uid := range accounts {
		uids = append(uids, int(uid))
	}
	sort.Ints(uids)
	parts := make([]string, 0, len(uids))
	for _, uid := range uids {
		parts = append(parts, fmt.Sprintf("%d=%s", uid, accounts[uint32(uid)]))
	}
	return strings.Join(parts, ",")
}

// hostAccount is who a socket connection's peer is: its uid, and the
// person it maps to ("" when it maps to nobody).
type hostAccount struct {
	uid    uint32
	person string
}

type hostPeerContextKey struct{}
type hostAccountContextKey struct{}

// hostPeer is what HostAccountConnContext read off one connection.
type hostPeer struct {
	uid uint32
	err error
}

// HostAccountConnContext is the API socket's http.Server.ConnContext: it
// reads the connecting process's uid off c (SO_PEERCRED) once per
// connection, for HostAccountHandler to map.
func HostAccountConnContext(ctx context.Context, c net.Conn) context.Context {
	uid, err := peerUID(c)
	return context.WithValue(ctx, hostPeerContextKey{}, hostPeer{uid: uid, err: err})
}

// hostAccountFromContext is the mapped host account HostAccountHandler
// admitted the request in ctx as, or nil for a request that never came
// over the socket.
func hostAccountFromContext(ctx context.Context) *hostAccount {
	if ctx == nil {
		return nil
	}
	if a, ok := ctx.Value(hostAccountContextKey{}).(*hostAccount); ok && a != nil && a.person != "" {
		return a
	}
	return nil
}

// hostAccountVerification is the verified identity a request admitted
// on the socket carries in place of any verifier's.
func hostAccountVerification(a *hostAccount) callerVerification {
	return callerVerification{
		present: true,
		id: VerifiedIdentity{
			Email: a.person,
			Sub:   a.person,
			Kind:  model.PrincipalHuman,
		},
		method: AuthMethodHostAccount,
	}
}

// socketStrippedHeaders are the credentials and identity claims a request
// on the socket loses: there it is who its uid says, and nobody else.
var socketStrippedHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"X-Grain-Tunnel-Token",
	AccessCodeHeader,
	AccessNameHeader,
	ActingTaskHeader,
	"X-Forwarded-User",
}

// hostSocketRemoteAddr is the RemoteAddr a request on the socket is
// given: never empty (which singleTenantTrusted takes for in-process),
// never an IP address (loopback or a test network).
func hostSocketRemoteAddr(uid uint32) string {
	return "unix-socket:uid=" + strconv.FormatUint(uint64(uid), 10)
}

// HostAccountHandler serves the API socket: each request's peer uid (read
// by HostAccountConnContext) is mapped through accounts(), and one that
// maps to nobody is refused here; one that does is handed to next as
// that person.
func (s *Server) HostAccountHandler(accounts func() HostAccounts) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())
		peer, ok := r.Context().Value(hostPeerContextKey{}).(hostPeer)
		if !ok {
			peer.err = errors.New("the connection's peer credentials were never read")
		}
		if peer.err != nil {
			writeError(w, http.StatusForbidden, fmt.Errorf("refused on the API socket: %v", peer.err))
			return
		}
		r = r.Clone(r.Context())
		// First, so nothing below -- a refusal included -- ever sees the
		// empty address net/http gives an unnamed unix peer.
		r.RemoteAddr = hostSocketRemoteAddr(peer.uid)
		var person string
		if accounts != nil {
			person = accounts()[peer.uid]
		}
		if person == "" {
			log.Printf("grain ui: refused uid %d on the API socket: -host-accounts maps it to nobody", peer.uid)
			writeError(w, http.StatusForbidden, fmt.Errorf(
				"uid %d is not mapped to a person on this daemon's API socket: an operator maps it with -host-accounts %d=<email> "+
					"(or -deployment-owner, for the daemon's own account); set %s=none to reach the daemon over -server instead",
				peer.uid, peer.uid, APISocketEnv))
			return
		}
		for _, h := range socketStrippedHeaders {
			r.Header.Del(h)
		}
		for _, h := range proxyIdentityHeaders {
			r.Header.Del(h)
		}
		for _, h := range forwardingHeaders {
			r.Header.Del(h)
		}
		for _, v := range registeredVerifiersSnapshot() {
			for _, h := range v.Headers() {
				r.Header.Del(h)
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), hostAccountContextKey{}, &hostAccount{uid: peer.uid, person: person}))
		s.ServeHTTP(w, r)
	})
}

// ListenAPISocket listens on path, in a directory only this account may
// enter (0700) unless an operator grants another account search on it,
// replacing a socket a previous run left there. Anything else at path is
// left alone and refused.
func ListenAPISocket(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := restrictAPISocketDir(dir, filepath.Base(path)); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// The directory is the gate (0700, plus whatever search an operator
	// grants another account with setfacl, docs/access-code.md), and the
	// uid map the second; the socket itself must be writable by every
	// account that gets through the directory, or connect(2) refuses the
	// very accounts -host-accounts names
	// (docs/test-runs/2026-11-11-caller-identity.md, F4).
	if err := os.Chmod(path, 0o666); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("opening %s to the accounts its directory admits: %w", path, err)
	}
	return ln, nil
}

// restrictAPISocketDir tightens the socket's directory on every start
// rather than trusting it to have stayed so: nobody but this account may
// write to it, and nobody it has not granted search (setfacl) may enter.
// Two things it must not do (docs/test-runs/2026-11-11-caller-identity.md,
// F5): chmod 0700 over a directory an operator had granted another account
// search on with setfacl, which zeroes the ACL mask and so revoked that
// grant on every restart; and chmod a directory anything else lives in --
// one somebody else shares (an -api-socket in /tmp), or one that holds
// other files (/run as root, $HOME or the data directory itself), whose
// mode was never this daemon's to set. Both are refused instead: the
// directory must hold nothing but the socket, sock.
func restrictAPISocketDir(dir, sock string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	perm := fi.Mode().Perm()
	if fi.Mode()&os.ModeSticky != 0 || perm&0o002 != 0 || !ownedByThisAccount(fi) {
		return fmt.Errorf("%s is shared with other accounts (mode %v): put the API socket in a directory of its own "+
			"(-api-socket <dir>/grain.sock, with <dir> new or this account's alone)", dir, fi.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != sock {
			return fmt.Errorf("%s holds other files (%s, ...): put the API socket in a directory of its own "+
				"(-api-socket <dir>/grain.sock, with <dir> new or holding only the socket)", dir, e.Name())
		}
	}
	// On a directory carrying an access ACL the group bits are its mask,
	// so group search is kept there and the mask a setfacl -m
	// u:<account>:x leaves stays --x. Without one they are the owning
	// group's own, and search for it would let that whole group reach the
	// socket, so the directory goes to 0700 -- and with an ACL the owning
	// group's own entry, which that --x mask would pass, is cleared
	// (clearOwningGroupACL). Group read and write, and everything for
	// others, go either way.
	keep := os.FileMode(0o700)
	if hasAccessACL(dir) {
		if err := clearOwningGroupACL(dir); err != nil {
			return err
		}
		keep = 0o710
	}
	if want := perm & keep; want != perm {
		if err := os.Chmod(dir, want); err != nil {
			return fmt.Errorf("restricting %s: %w", dir, err)
		}
	}
	return nil
}

// HostAPISocket is the socket the host CLI dials in place of a loopback
// -server: $GRAIN_API_SOCKET, else the one under $GRAIN_DATA_DIR, when it
// is a socket this process can reach; "" otherwise.
func HostAPISocket() string {
	path := strings.TrimSpace(os.Getenv(APISocketEnv))
	if strings.EqualFold(path, "none") || strings.EqualFold(path, "off") {
		return ""
	}
	if path == "" {
		dir := strings.TrimSpace(os.Getenv("GRAIN_DATA_DIR"))
		if dir == "" {
			return ""
		}
		path = APISocketPath(dir)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return path
}
