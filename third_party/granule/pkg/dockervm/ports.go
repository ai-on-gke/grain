package dockervm

import (
	"bytes"
	"context"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/bwsalmon/granule/pkg/guestimage"
)

// The reporting half of EXPOSE: what a VM's container actually
// publishes, asked of docker after the fact.
//
// It is asked rather than remembered because the host side is docker's
// to choose (see publishArgs): a VM created with -publish-declared is
// saved as a grant, so the only place the host ports exist is on the
// running container. "vm create" prints them once, at create time --
// this is the same question asked later, by "vm status" and "vm list
// -status", so that knowing which host port reaches a guest does not
// require knowing granule's container naming to type "docker port
// granule-vm-<name>" by hand.

// PublishedPort is one mapping docker reports for a container: a port
// inside the guest, and one address out here that reaches it.
type PublishedPort struct {
	// Guest is the port the guest listens on, in docker's own spelling
	// ("8080/tcp") -- the same string the image declared with EXPOSE.
	Guest string

	// Host is where it answers on this machine ("0.0.0.0:32768"), the
	// address included: a publication bound to one interface and one
	// bound to every interface are different facts about who can reach
	// the guest. One guest port commonly has two of these, an IPv4 and
	// an IPv6 binding of the same host port.
	Host string
}

// String is the mapping the way docker itself prints it, which is what
// "vm status" shows: a caller who then goes and runs "docker port" sees
// the same line back.
func (p PublishedPort) String() string { return p.Guest + " -> " + p.Host }

// Describe is String with who can reach the binding said in words --
// "8080/tcp -> 0.0.0.0:32768 (every interface)" -- which is what "vm
// create" and "vm status" print. The address alone is the whole answer
// to somebody who reads 0.0.0.0 fluently, and the words are for
// everybody else: a port open to every network the host is on should
// not depend on its reader knowing that.
func (p PublishedPort) Describe() string {
	addr := p.Host
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		addr = addr[:i]
	}
	addr = strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
	return p.String() + " (" + bindScope(addr) + ")"
}

// bindScope is what a host bind address means for who can reach it.
func bindScope(addr string) string {
	ip := net.ParseIP(addr)
	switch {
	case ip == nil:
		return "bound to " + addr
	case ip.IsUnspecified():
		return "every interface"
	case ip.IsLoopback():
		return "this host only"
	default:
		return "only via " + addr
	}
}

// HostPort is the host side without its address ("32768"), for the
// places that have room for the mapping but not for the binding -- see
// "vm list -status"' own column.
func (p PublishedPort) HostPort() string {
	// The last colon rather than the first: an IPv6 binding is
	// "[::]:32768", which has several.
	if i := strings.LastIndex(p.Host, ":"); i >= 0 {
		return p.Host[i+1:]
	}
	return p.Host
}

// Published is what VM name's container publishes, as docker reports it.
//
// A container that isn't there at all publishes nothing, and that is
// reported as an empty list rather than as an error -- to a caller
// reporting on a VM it is a fact about the VM (the same reading
// containerState takes of a missing container). A container that has
// exited comes back empty too: docker only holds a mapping for as long
// as it holds the publication, which is another way of saying
// publication belongs to a running container.
func Published(ctx context.Context, d *Docker, name string) ([]PublishedPort, error) {
	var out bytes.Buffer
	if err := d.run(ctx, &out, "port", vmContainerName(name)); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return parsePublished(out.String()), nil
}

// parsePublished reads "docker port"'s output, whose every line is
// "<port>/<proto> -> <host address>". Anything else in there is skipped
// rather than guessed at: this is another program's output, and a line
// this doesn't recognize is a docker that has learned to say something
// new, not a port to report half of.
func parsePublished(out string) []PublishedPort {
	var ports []PublishedPort
	for _, line := range strings.Split(out, "\n") {
		guest, host, ok := strings.Cut(strings.TrimSpace(line), "->")
		if !ok {
			continue
		}
		guest, host = strings.TrimSpace(guest), strings.TrimSpace(host)
		if guest == "" || host == "" {
			continue
		}
		ports = append(ports, PublishedPort{Guest: guest, Host: host})
	}
	// Docker's own order is whatever its map iteration produced, and
	// this ends up in front of a person reading two VMs' status side by
	// side -- so it is sorted, by the guest port as a number rather than
	// as text (9/tcp before 80/tcp before 8080/tcp).
	sort.SliceStable(ports, func(i, j int) bool {
		gi, gj := guestPortNumber(ports[i].Guest), guestPortNumber(ports[j].Guest)
		if gi != gj {
			return gi < gj
		}
		if ports[i].Guest != ports[j].Guest {
			return ports[i].Guest < ports[j].Guest
		}
		return ports[i].Host < ports[j].Host
	})
	return ports
}

// guestPortNumber is the number in "8080/tcp", or -1 for anything that
// isn't one, which sorts such a line to the top rather than losing it.
func guestPortNumber(guest string) int {
	num, _, _ := strings.Cut(guest, "/")
	n, err := strconv.Atoi(num)
	if err != nil {
		return -1
	}
	return n
}

// DeclaredPorts is what the image a VM runs asks for with EXPOSE, read
// with the same docker this package drives everything else through.
//
// It is the other half of the answer "vm status" gives about ports, and
// only consulted when nothing is published: an image that declares ports
// nobody granted and an image that declares none look identical from
// outside ("this VM publishes nothing"), and they are the confusion
// -publish-declared invites. Reading the declaration is not honouring it
// -- see pkg/guestimage, and the README's "Publishing a guest's
// ports".
func DeclaredPorts(ctx context.Context, d *Docker, ref string) ([]string, error) {
	return guestimage.DeclaredPorts(ctx, d.binary(), ref, nil)
}
