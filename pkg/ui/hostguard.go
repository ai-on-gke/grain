package ui

import (
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

// HostGuard wraps next so that a request whose Host header names a host
// this listener does not answer to is refused with 421 Misdirected
// Request before any routing happens (grain#2380).
//
// What it defends against is DNS rebinding: a page on a domain an
// attacker controls re-resolves that domain to 127.0.0.1 (or the
// daemon's LAN address) once its script has loaded, and from then on
// the browser treats the daemon as that page's own origin -- every
// fetch() it makes lands here, carrying the attacker's domain as its
// Host. "Bound to loopback" is only the boundary it looks like if a Host
// that is not one of this daemon's own names is refused.
//
// So a Host is allowed when it is:
//
//   - absent (HTTP/1.0), or an IP literal -- rebinding needs a DNS name
//     the attacker controls, and an IP-literal origin is one no attacker
//     page can be served from and then rebound;
//   - localhost or any *.localhost name, which resolve to loopback only;
//   - this machine's own hostname, short or fully qualified;
//   - a *.ts.net name -- `tailscale serve` (scripts/setup.sh's
//     setup_tailscale) proxies the tailnet onto the loopback listener
//     with the node's MagicDNS name as Host, and those names are
//     answered by Tailscale's DNS, not one an attacker can point at
//     127.0.0.1;
//   - the host part of listenAddr, when that is a name; or
//   - named by allowed: exact names ("grain.example.com"), or a leading
//     "*." for every name below a domain ("*.example.com"). A port on an
//     entry is ignored, as it is on the Host being checked.
//
// The check is enforced when allowed is non-empty, or when listenAddr is
// bound to one specific address (127.0.0.1:80, say). A listener bound to
// every interface (the daemon's own 0.0.0.0:4242 default, or
// deploy/kubernetes' 0.0.0.0:8080) is one an operator has put on a
// network under whatever names that network gives it, which grain cannot
// guess, so until -ui-allowed-hosts names them only the requests that
// arrive over loopback are checked: those are the ones a page rebound to
// 127.0.0.1 would be worth sending. Every other client has to
// sign in, and a rebound page carries none of this daemon's cookies.
// allowed containing "*" turns the check off outright.
func HostGuard(next http.Handler, listenAddr string, allowed []string) http.Handler {
	g, enforce := newHostGuard(listenAddr, allowed, osHostnames())
	if !enforce && !g.loopbackOnly {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enforce && !fromLoopback(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !g.allows(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, g.misdirected(r.Host))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HostGuardEnforced reports whether HostGuard would check Host headers
// on every request for this listenAddr and allowed list, so a caller can
// say so in its own startup log. False includes the wildcard listener
// whose loopback requests alone are checked.
func HostGuardEnforced(listenAddr string, allowed []string) bool {
	_, enforce := newHostGuard(listenAddr, allowed, nil)
	return enforce
}

// HostFamily is a family of names some environment's own web proxy
// serves the UI under -- a workstation's corp proxy, say -- which an
// operator admits with one wildcard -ui-allowed-hosts entry. Registering
// one (RegisterHostFamily) admits nothing by itself: what it changes is
// the 421 a refused name from the family gets, which then says how that
// environment's installer admits it rather than naming scripts/setup.sh.
type HostFamily struct {
	// Pattern is the -ui-allowed-hosts entry that admits the family: a
	// leading "*." and the domain below which every name belongs to it.
	Pattern string
	// Advice is what follows "is not one this grain daemon answers to; "
	// in the refusal, given the GRAIN_UI_ALLOWED_HOSTS list that would
	// admit the name (the hosts already allowed, plus Pattern).
	Advice func(rerunHosts string) string
}

var (
	hostFamiliesMu sync.RWMutex
	hostFamilies   = map[string]HostFamily{}
)

// RegisterHostFamily adds f, replacing any family with the same Pattern,
// and returns a function that removes it again (for tests).
func RegisterHostFamily(f HostFamily) (unregister func()) {
	hostFamiliesMu.Lock()
	defer hostFamiliesMu.Unlock()
	hostFamilies[f.Pattern] = f
	return func() {
		hostFamiliesMu.Lock()
		defer hostFamiliesMu.Unlock()
		delete(hostFamilies, f.Pattern)
	}
}

// hostFamilyOf is the registered family h (a normalized host) belongs to.
func hostFamilyOf(h string) (HostFamily, bool) {
	hostFamiliesMu.RLock()
	defer hostFamiliesMu.RUnlock()
	patterns := make([]string, 0, len(hostFamilies))
	for p := range hostFamilies {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	for _, p := range patterns {
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(h, strings.ToLower(p[1:])) {
			return hostFamilies[p], true
		}
	}
	return HostFamily{}, false
}

// misdirectedHostError is the 421 HostGuard answers with. rerunHosts,
// when set, is the GRAIN_UI_ALLOWED_HOSTS an install would need
// re-running with for host to be allowed; family, when set, is the
// registered HostFamily host belongs to, whose own Advice says how.
type misdirectedHostError struct {
	host, rerunHosts string
	family           *HostFamily
}

func (e misdirectedHostError) Error() string {
	msg := "host " + e.host + " is not one this grain daemon answers to; "
	if e.family != nil && e.family.Advice != nil {
		return msg + e.family.Advice(e.rerunHosts)
	}
	if e.rerunHosts != "" {
		return msg + "if it should be, re-run the installer allowing it (the list replaces the one installed, " +
			"so it keeps the names already allowed; this machine's own names are always answered): " +
			"sudo GRAIN_UI_ALLOWED_HOSTS='" + e.rerunHosts + "' scripts/setup.sh " +
			"(or give the daemon -ui-allowed-hosts naming it, e.g. grain init -daemon-arg -ui-allowed-hosts=" + e.rerunHosts + ")"
	}
	return msg + "if it should be, re-run the installer with GRAIN_UI_ALLOWED_HOSTS naming it " +
		"(or give the daemon -ui-allowed-hosts naming it)"
}

// misdirected is the error refusing host, with the installer command
// that would admit it. A name in a registered HostFamily gets the allowed
// hosts already named (else this machine's own names), plus the family's
// pattern, for that family's own advice; any other name gets the hosts
// already named plus itself, for scripts/setup.sh -- GRAIN_UI_ALLOWED_HOSTS
// replaces the installed list rather than adding to it. A Host that is
// not a plain DNS name is not pasted into a command at all.
func (g hostGuard) misdirected(host string) error {
	e := misdirectedHostError{host: host}
	h := normalizeHost(host)
	if f, ok := hostFamilyOf(h); ok {
		names := g.named
		if len(names) == 0 {
			names = g.hostnames
		}
		e.family = &f
		e.rerunHosts = strings.Join(append(append([]string(nil), names...), f.Pattern), ",")
	} else if plainHostName(h) {
		e.rerunHosts = strings.Join(append(append([]string(nil), g.named...), h), ",")
	}
	return e
}

// plainHostName is whether h is made of nothing but DNS name characters,
// and so safe to quote into a suggested shell command.
func plainHostName(h string) bool {
	if h == "" {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

type hostGuard struct {
	exact    map[string]bool
	suffixes []string
	// named is the allowed entries as given, and hostnames this
	// machine's own names, for the command misdirected suggests.
	named, hostnames []string
	// loopbackOnly is a listener bound to every interface with nothing
	// in allowed: HostGuard checks only the requests that arrive over
	// loopback.
	loopbackOnly bool
}

// fromLoopback is whether r's peer is this host's own loopback.
func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newHostGuard(listenAddr string, allowed, hostnames []string) (hostGuard, bool) {
	g := hostGuard{exact: map[string]bool{"localhost": true}, suffixes: []string{".localhost", ".ts.net"}, hostnames: hostnames}
	for _, h := range hostnames {
		g.add(h)
	}
	listenHost := listenAddr
	if h, _, err := net.SplitHostPort(listenAddr); err == nil {
		listenHost = h
	}
	wildcard := listenHost == "" || listenHost == "0.0.0.0" || listenHost == "::"
	if !wildcard {
		g.add(listenHost)
	}
	explicit := false
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if a == "*" {
			return g, false
		}
		explicit = true
		g.named = append(g.named, a)
		g.add(a)
	}
	g.loopbackOnly = wildcard && !explicit
	return g, explicit || !wildcard
}

func (g *hostGuard) add(entry string) {
	entry = normalizeHost(entry)
	if entry == "" {
		return
	}
	if strings.HasPrefix(entry, "*.") {
		g.suffixes = append(g.suffixes, entry[1:])
		return
	}
	g.exact[entry] = true
}

func (g hostGuard) allows(host string) bool {
	h := normalizeHost(host)
	if h == "" || net.ParseIP(h) != nil || g.exact[h] {
		return true
	}
	for _, s := range g.suffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// normalizeHost lowercases host and drops any port, IPv6 brackets and
// trailing root dot, so "LocalHost.:8420" and "[::1]:80" compare as the
// names they are.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.TrimSuffix(host, ".")
}

// osHostnames is this machine's own name, and its first label when that
// name is fully qualified -- the two forms a browser on the same LAN
// would put in Host.
func osHostnames() []string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return nil
	}
	names := []string{name}
	if short, _, ok := strings.Cut(name, "."); ok && short != "" {
		names = append(names, short)
	}
	return names
}
