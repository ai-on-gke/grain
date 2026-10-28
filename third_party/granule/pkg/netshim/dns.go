package netshim

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
)

// AutoDNS is the NETSHIM_DNS value (and VMSpec.DNS, and granule's -dns)
// that asks netshim to point the guest at the sandbox's own resolver --
// the nameservers in the network namespace's /etc/resolv.conf -- when the
// guest can reach them, and at FallbackDNS when it cannot. Leaving
// NETSHIM_DNS unset means the same thing. See ResolveDNS.
const AutoDNS = "auto"

// FallbackDNS4 and FallbackDNS6 are the nameservers an AutoDNS guest is
// given when the namespace's own resolv.conf names nothing the guest can
// route to -- a public resolver, because it is the only answer that is
// right on an arbitrary host. The two resolvers that case is made of are
// docker's embedded one on 127.0.0.11, which is the namespace's own
// loopback and not on the wire, and a link-local stub such as GCE's
// metadata server on 169.254.169.254, which answers only on the host's
// own segment. A guest on a tap reaches neither, and a guest pointed at
// one has open IP egress and hangs on every name it looks up.
//
// Which of the two is the guest's family: see FallbackDNS.
const (
	FallbackDNS4 = "8.8.8.8"
	FallbackDNS6 = "2001:4860:4860::8888"
)

// FallbackDNS returns the fallback nameserver for a guest holding id:
// FallbackDNS4 whenever it has IPv4, dual stack included, which is what
// every guest got before IPv6 existed; FallbackDNS6 when it is IPv6
// only, since an IPv4 resolver is one it has no route to, and the hang
// FallbackDNS4 exists to avoid is exactly what it would get.
func FallbackDNS(id Identity) net.IP {
	if !id.HasIPv4() && id.HasIPv6() {
		return net.ParseIP(FallbackDNS6)
	}
	return net.ParseIP(FallbackDNS4).To4()
}

// The limits a search list is held to before it goes on the kernel
// command line: glibc's historical ones (MAXDNSRCH and the 256-byte
// buffer it parsed the list into), which musl keeps to this day. A guest
// resolver would drop what is past them anyway, and they keep what the
// list adds to a cmdline the kernel caps at 2048 bytes small.
const (
	maxDNSSearch    = 6
	maxDNSSearchLen = 256
	// maxNdots is the largest ndots glibc and musl honour; both clamp a
	// larger value to it.
	maxNdots = 15
)

// resolvConfPath is the file ResolveDNS reads. A variable so tests can
// point it at a file of their own.
var resolvConfPath = "/etc/resolv.conf"

// IsAutoDNS reports whether spec, a NETSHIM_DNS or VMSpec.DNS value, asks
// for the sandbox's own resolver rather than naming nameservers.
func IsAutoDNS(spec string) bool {
	return strings.TrimSpace(spec) == AutoDNS
}

// ValidateDNS checks a NETSHIM_DNS or VMSpec.DNS value: AutoDNS, or
// something ParseDNS accepts.
func ValidateDNS(spec string) error {
	if IsAutoDNS(spec) {
		return nil
	}
	_, err := ParseDNS(spec)
	return err
}

// ResolvConf is the part of a resolv.conf a guest is handed.
type ResolvConf struct {
	Nameservers []net.IP
	Search      []string
	// Options is the file's ndots option, as "ndots:n", or nothing. No
	// other option is carried: the rest either mean nothing to a guest
	// with a single interface or are not the sandbox's to impose.
	Options []string
}

// ParseResolvConf reads the nameserver, search (and domain) lines and
// the ndots option of a resolv.conf. It keeps every nameserver it can parse
// as an address, routable or not; GuestResolvers is what filters them.
// As in glibc, the last search or domain line wins.
func ParseResolvConf(data []byte) ResolvConf {
	var rc ResolvConf
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			if len(fields) > 1 {
				if ip := net.ParseIP(fields[1]); ip != nil {
					rc.Nameservers = append(rc.Nameservers, ip)
				}
			}
		case "search", "domain":
			rc.Search = append([]string(nil), fields[1:]...)
		case "options":
			for _, opt := range fields[1:] {
				if v, ok := strings.CutPrefix(opt, "ndots:"); ok {
					if n, err := strconv.Atoi(v); err == nil && n >= 0 {
						rc.Options = []string{fmt.Sprintf("ndots:%d", min(n, maxNdots))}
					}
				}
			}
		}
	}
	return rc
}

// GuestResolvers returns the nameservers in rc that a guest holding id,
// spliced onto the sandbox's interface, can actually reach, in rc's
// order: at most maxDNS of them, and at most maxDNS4 of those IPv4 (the
// most ip= can carry to an image that predates granule.dns=).
//
// Only the families the guest has an address in are kept. An IPv4
// nameserver is dropped when it is loopback (docker's 127.0.0.11,
// systemd-resolved's 127.0.0.53, which are the namespace's own loopback
// and not the guest's) or link-local (a cloud metadata resolver such as
// GCE's 169.254.169.254, on no segment the guest is on). An IPv6 one is
// dropped when it is ::1 or :: for the same reason, or link-local
// (fe80::/10), which would need a zone index resolv.conf cannot carry
// portably and is on no segment the guest is on either; global and ULA
// addresses are kept. A Kubernetes pod's cluster DNS ClusterIP passes --
// kind's fd00:10:96::a on a v6-only cluster, one of each family on a
// dual-stack one: the guest holds the pod's addresses and route table,
// so it reaches the ClusterIP exactly as the pod would have.
func GuestResolvers(rc ResolvConf, id Identity) []net.IP {
	var out []net.IP
	v4 := 0
	for _, ip := range rc.Nameservers {
		if ip4 := ip.To4(); ip4 != nil {
			if !id.HasIPv4() || v4 == maxDNS4 || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() {
				continue
			}
			v4++
			out = append(out, ip4)
		} else {
			if !id.HasIPv6() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			out = append(out, ip)
		}
		if len(out) == maxDNS {
			break
		}
	}
	return out
}

// guestSearch returns the search domains of rc a guest can be given on
// its command line: names of nothing but letters, digits, '-', '_' and
// '.', within the maxDNSSearch and maxDNSSearchLen budget. A domain past
// the budget is dropped along with everything after it, in order, which
// is what glibc would have done with the same list.
func guestSearch(search []string) []string {
	var out []string
	total := 0
	for _, d := range search {
		if !validSearchDomain(d) {
			continue
		}
		if len(out) == maxDNSSearch || total+len(d)+1 > maxDNSSearchLen {
			break
		}
		out = append(out, d)
		total += len(d) + 1
	}
	return out
}

func validSearchDomain(d string) bool {
	if d == "" || d == "." {
		return false
	}
	for _, r := range d {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// ResolveDNS returns c with an AutoDNS setting turned into nameservers
// for a guest holding id, the identity discovered on the external
// interface. A Config whose DNS was named explicitly (including named as
// nothing) is returned untouched.
//
// It reads the network namespace's own /etc/resolv.conf, so it must run
// inside the sandbox -- in the VM container, where netshim does -- and
// not in "granule vm create" on the host, whose resolver is a different
// one. When that file names a resolver the guest can reach (GuestResolvers),
// the guest gets it, along with the file's search list and ndots, so a
// Kubernetes guest resolves short Service names the way the pod would.
// Otherwise -- docker's embedded resolver, a link-local one, no file at
// all -- it gets the fallback for its family (FallbackDNS) and no search
// list, which for a guest with IPv4 is what every guest got before
// AutoDNS existed.
func (c Config) ResolveDNS(id Identity) Config {
	if !c.DNSAuto {
		return c
	}
	c.DNSAuto = false
	c.DNSSearch, c.DNSOptions = nil, nil

	fallback := FallbackDNS(id)
	data, err := os.ReadFile(resolvConfPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("dns: reading %s: %v; the guest resolves through the fallback %s", resolvConfPath, err, fallback)
	}
	rc := ParseResolvConf(data)
	if ns := GuestResolvers(rc, id); len(ns) > 0 {
		c.DNS = ns
		c.DNSSearch = guestSearch(rc.Search)
		c.DNSOptions = rc.Options
		return c
	}
	if len(rc.Nameservers) > 0 {
		log.Printf("dns: no nameserver in %s (%s) is reachable from the guest (%s); it resolves through the fallback %s",
			resolvConfPath, JoinIPs(rc.Nameservers), id.Addresses(), fallback)
	} else if err == nil || os.IsNotExist(err) {
		log.Printf("dns: %s names no nameserver; the guest resolves through the fallback %s", resolvConfPath, fallback)
	}
	c.DNS = []net.IP{fallback}
	return c
}

// JoinIPs renders nameservers space separated, for a log line.
func JoinIPs(ips []net.IP) string {
	s := make([]string, len(ips))
	for i, ip := range ips {
		s[i] = ip.String()
	}
	return strings.Join(s, " ")
}

// dnsParam renders nameservers as the granule.dns= kernel parameter,
// comma separated in their order, or "" when ip= says all of it
// already: no IPv6 nameserver among them and an ip= to carry them on.
// That keeps an IPv4 guest's command line exactly what it was before
// granule.dns= existed, and a guest image that predates it loses
// nothing it could have used.
func dnsParam(dns []net.IP, onIP bool) string {
	if len(dns) == 0 {
		return ""
	}
	s := make([]string, len(dns))
	all4 := true
	for i, ip := range dns {
		s[i] = ip.String()
		if ip.To4() == nil {
			all4 = false
		}
	}
	if onIP && all4 {
		return ""
	}
	return dnsParamKey + "=" + strings.Join(s, ",")
}

// dnsSearchParam renders the search list as the granule.dns_search=
// kernel parameter the guest's granule-configure-dns reads, or "" for an
// empty one.
func dnsSearchParam(search []string) string {
	if len(search) == 0 {
		return ""
	}
	return dnsSearchParamKey + "=" + strings.Join(search, ",")
}

// dnsOptionsParam renders resolver options as the granule.dns_options=
// kernel parameter, or "" for none.
func dnsOptionsParam(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	return dnsOptionsParamKey + "=" + strings.Join(opts, ",")
}
