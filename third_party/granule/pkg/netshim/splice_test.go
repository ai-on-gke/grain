package netshim

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// testFrame is an Ethernet frame carrying a locally-assigned experimental
// ethertype (0x88b5), chosen so the kernel's own protocol handlers ignore
// it and the only thing that can move it between the two devices is the
// splice itself.
func testFrame(payload string) []byte {
	frame := []byte{
		0x02, 0x00, 0x00, 0x00, 0x00, 0x02, // dst mac
		0x02, 0x00, 0x00, 0x00, 0x00, 0x01, // src mac
		0x88, 0xb5, // ethertype
	}
	frame = append(frame, []byte(payload)...)
	// Pad to the 60-byte Ethernet minimum so nothing along the way pads
	// it for us and changes the bytes we compare against.
	for len(frame) < 60 {
		frame = append(frame, 0)
	}
	return frame
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// openTapFD attaches to an already-created persistent tap, returning a
// file descriptor for it. Opening it is also what gives the tap a
// carrier: until some process holds it open, frames redirected into it
// are dropped, which is exactly the coupling the flat-mode setup has to
// cloud-hypervisor being up.
func openTapFD(t *testing.T, name string) int {
	t.Helper()
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open /dev/net/tun: %v", err)
	}
	t.Cleanup(func() { unix.Close(fd) })

	var req struct {
		name  [unix.IFNAMSIZ]byte
		flags uint16
		_     [22]byte
	}
	copy(req.name[:], name)
	req.flags = unix.IFF_TAP | unix.IFF_NO_PI
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
		uintptr(unix.TUNSETIFF), uintptr(unsafe.Pointer(&req))); errno != 0 {
		t.Fatalf("TUNSETIFF %s: %v", name, errno)
	}
	return fd
}

// openPacketSocket returns a raw AF_PACKET socket bound to the named
// interface, for injecting and capturing frames on it directly.
func openPacketSocket(t *testing.T, index int) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("AF_PACKET socket: %v", err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_ALL),
		Ifindex:  index,
	}); err != nil {
		t.Fatalf("bind AF_PACKET to index %d: %v", index, err)
	}
	return fd
}

const (
	// spliceWaitBudget bounds how long one direction of the splice has
	// to deliver a frame before the test calls it broken. A working
	// splice takes microseconds, so this is not a measurement of
	// anything: it is slack for a CI runner that is sharing its CPUs
	// with someone else, where the cost of waiting is nothing and the
	// cost of giving up too early is a red build on a change that broke
	// nothing.
	spliceWaitBudget = 10 * time.Second

	// spliceResendInterval is how often awaitFrame re-sends the frame
	// while it waits. A frame put on the wire before the kernel has
	// settled -- a device whose carrier is not up yet, an ingress path
	// that has not picked up the filter -- is dropped silently and
	// never retried by anything, so sending exactly once makes this
	// test a coin toss over that window. A resend costs one 60-byte
	// frame per tick and leaves only a splice that genuinely is not
	// carrying traffic able to fail.
	spliceResendInterval = 100 * time.Millisecond

	// spliceReadyBudget bounds the wait for the links to report
	// themselves carrying and filtered before any frame is sent. It
	// matches spliceWaitBudget for the same reason: a veth's carrier
	// comes from deferred linkwatch work, so how long this takes is a
	// property of how busy the machine is rather than of the splice,
	// and giving up early would trade one flaky failure for another.
	spliceReadyBudget = 10 * time.Second
)

// splicedPair names one direction of the splice under test: frames
// arriving on from are redirected out to.
type splicedPair struct{ from, to string }

// observation is what awaitFrame saw while it waited, so a failure can
// distinguish "nothing at all arrived" from "frames arrived but never the
// one we sent" -- and name the errno if the wait ended on a failed read.
type observation struct {
	sends    int   // frames handed to the sender
	retries  int   // sends that returned a transient errno and were retried
	frames   int   // frames read off the receiving fd, matching or not
	lastSend error // the last transient send errno, if any
	sendErr  error // the send error that ended the wait, if any
	readErr  error // the read error that ended the wait, if any
	elapsed  time.Duration
}

func (o observation) String() string {
	s := fmt.Sprintf("sent %d frame(s) over %s, read %d frame(s) on the receiving end, none matching",
		o.sends, o.elapsed.Round(time.Millisecond), o.frames)
	if o.retries > 0 {
		s += fmt.Sprintf("; %d send(s) retried, last: %v", o.retries, o.lastSend)
	}
	if o.sendErr != nil {
		s += fmt.Sprintf("; send failed: %v", o.sendErr)
	}
	if o.readErr != nil {
		s += fmt.Sprintf("; read failed: %v", o.readErr)
	}
	return s
}

// awaitFrame sends a frame with send, repeatedly, until fd yields one
// containing want or the budget runs out, reporting what it saw either
// way.
//
// Both sources are noisy -- an AF_PACKET socket sees the frames the test
// itself sent out that interface, and a live namespace carries unrelated
// background traffic -- so it matches on the payload rather than taking
// the first frame to arrive. EINTR is not a failure, on either side of
// the exchange: the Go runtime preempts goroutines with a signal, so any
// syscall here can be interrupted at any moment, and treating that as
// "the frame never came" would fail the test for a reason that has
// nothing to do with the splice. The send is retried on the same terms,
// for that reason plus one of its own: a raw socket whose transmit queue
// is momentarily full answers with EAGAIN or ENOBUFS, which says nothing
// about the splice either, and it is the one syscall here still able to
// fail this test outright.
//
// The resend loop is what actually holds this test up, and the numbers
// are worth keeping: measured on a GitHub runner (Ubuntu, kernel 6.17),
// 28 of 200 frames sent the instant after the splice was installed were
// dropped -- mirred counted the packet and raised overlimits, meaning
// the redirect itself failed -- and every one of them arrived on the
// retry a millisecond later. That is the same rate at which the
// send-once version of this test failed CI.
func awaitFrame(fd int, send func() error, want []byte) (bool, observation) {
	var obs observation
	buf := make([]byte, 2048)
	start := time.Now()
	deadline := start.Add(spliceWaitBudget)
	var nextSend time.Time

	for time.Now().Before(deadline) {
		if now := time.Now(); !now.Before(nextSend) {
			switch err := send(); {
			case err == nil:
				obs.sends++
				nextSend = now.Add(spliceResendInterval)
			case transient(err):
				obs.retries++
				obs.lastSend = err
				time.Sleep(2 * time.Millisecond)
				continue
			default:
				obs.sendErr = err
				obs.elapsed = time.Since(start)
				return false, obs
			}
		}

		n, err := unix.Read(fd, buf)
		switch {
		case transient(err):
			time.Sleep(2 * time.Millisecond)
			continue
		case err != nil:
			// Anything else is the fd itself being unusable, which
			// no amount of waiting will fix.
			obs.readErr = err
			obs.elapsed = time.Since(start)
			return false, obs
		}
		obs.frames++
		if n > 0 && bytes.Contains(buf[:n], want) {
			obs.elapsed = time.Since(start)
			return true, obs
		}
	}

	obs.elapsed = time.Since(start)
	return false, obs
}

// transient reports whether err is a syscall failing for a reason that
// has nothing to do with what is being tested and that another attempt
// can get past: the read or write was interrupted by a signal, or a
// non-blocking fd had nothing to give and no room to take.
func transient(err error) bool {
	// EWOULDBLOCK is not listed beside EAGAIN: on Linux they are the
	// same errno, and naming both is a compile error.
	switch err {
	case unix.EINTR, unix.EAGAIN, unix.ENOBUFS:
		return true
	}
	return false
}

// waitSpliceReady blocks until the kernel reports the splice able to
// carry a frame at all: every link both administratively up and with a
// carrier, and each spliced link carrying exactly one ingress filter
// that redirects to the link it is spliced to.
//
// None of it is cosmetic. mirred drops a redirected frame outright --
// "tc mirred to Houston: device %s is down" in dmesg, nothing anywhere
// else -- if the device it redirects to is not up and carrying, and a
// tap has no carrier until some process holds it open. Sending before
// that settles loses the frame silently, which is indistinguishable
// from a splice that does not work at all.
//
// It is a necessary condition and not a sufficient one, so do not read
// it as a licence to send exactly once: on a 6.17 kernel one frame in
// seven sent the moment every link reported itself up, carrying and
// correctly filtered was still dropped by mirred. Nothing netlink
// reports narrows that window further, which is why awaitFrame resends.
func waitSpliceReady(t *testing.T, spliced []splicedPair, alsoUp []string) {
	t.Helper()
	deadline := time.Now().Add(spliceReadyBudget)
	for {
		reason := spliceNotReady(spliced, alsoUp)
		if reason == "" {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("splice not ready after %s: %s\n%s", spliceReadyBudget,
				reason, describeLinks(spliceLinkNames(spliced, alsoUp)...))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// spliceLinkNames is every link the splice involves, each named once.
func spliceLinkNames(spliced []splicedPair, alsoUp []string) []string {
	var names []string
	seen := map[string]bool{}
	for _, name := range append(spliceEnds(spliced), alsoUp...) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func spliceEnds(spliced []splicedPair) []string {
	var names []string
	for _, pair := range spliced {
		names = append(names, pair.from, pair.to)
	}
	return names
}

// spliceNotReady returns why the splice is not yet ready to carry a
// frame, or "" once it is.
//
// The carrier is read from IFF_LOWER_UP rather than IFF_RUNNING, which
// looks like the obvious flag and is a trap: IFF_RUNNING is derived from
// the link's operstate, and operstate is updated by deferred linkwatch
// work, so a tap that nothing holds open reports RUNNING with operstate
// still "unknown" for a moment after it is created -- while frames
// redirected into it are already being dropped. IFF_LOWER_UP is the
// carrier bit itself, which is what mirred actually tests, and it is
// correct immediately.
//
// The filter is checked by where it redirects, not just by there being
// one of it. A filter counts an ifindex, not a name, so one left over
// from an earlier device that carried the same name -- what a rerun of
// the test, or of Setup, can leave behind -- passes a bare count and
// then drops every frame it is handed. Waiting for the redirect to name
// the link it is supposed to name costs nothing and turns that into a
// failure that says so, rather than 10s of resends into a black hole
// and the same "never arrived" as a splice that was never installed.
func spliceNotReady(spliced []splicedPair, alsoUp []string) string {
	for _, name := range spliceLinkNames(spliced, alsoUp) {
		link, err := netlink.LinkByName(name)
		if err != nil {
			return fmt.Sprintf("looking up %s: %v", name, err)
		}
		attrs := link.Attrs()
		if attrs.Flags&net.FlagUp == 0 {
			return fmt.Sprintf("%s is not up", name)
		}
		if attrs.RawFlags&unix.IFF_LOWER_UP == 0 {
			return fmt.Sprintf("%s has no carrier (oper %s)", name, attrs.OperState)
		}
	}
	for _, pair := range spliced {
		from, err := netlink.LinkByName(pair.from)
		if err != nil {
			return fmt.Sprintf("looking up %s: %v", pair.from, err)
		}
		to, err := netlink.LinkByName(pair.to)
		if err != nil {
			return fmt.Sprintf("looking up %s: %v", pair.to, err)
		}
		filters, err := netlink.FilterList(from, netlink.MakeHandle(0xffff, 0))
		if err != nil {
			return fmt.Sprintf("listing ingress filters on %s: %v", pair.from, err)
		}
		if len(filters) != 1 {
			return fmt.Sprintf("%s has %d ingress filters, want 1", pair.from, len(filters))
		}
		target, ok := redirectTarget(filters[0])
		if !ok {
			return fmt.Sprintf("%s's ingress filter does not redirect anywhere: %s",
				pair.from, describeFilter(filters[0]))
		}
		if target != to.Attrs().Index {
			return fmt.Sprintf("%s redirects to ifindex %d, want %s (ifindex %d)",
				pair.from, target, pair.to, to.Attrs().Index)
		}
	}
	return ""
}

// redirectTarget returns the ifindex a filter's mirred action redirects
// to, and whether it has one at all.
func redirectTarget(filter netlink.Filter) (int, bool) {
	u32, ok := filter.(*netlink.U32)
	if !ok {
		return 0, false
	}
	for _, action := range u32.Actions {
		if mirred, ok := action.(*netlink.MirredAction); ok {
			return mirred.Ifindex, true
		}
	}
	return 0, false
}

// describeLinks renders what the kernel thinks of each named link and of
// the ingress filters on it, for a failure message that says what was
// observed rather than only what was expected.
//
// The mirred action's own packet counter is the useful part: it
// separates the two ways this test can fail. An action that counted no
// packets means the frame never reached the filter, so the fault is
// upstream of the splice (or the frame was never sent); one that counted
// packets means the splice ran and the frame was lost past it -- and the
// link flags beside it usually say why, since a redirect to a device
// without a carrier is dropped and counted.
func describeLinks(names ...string) string {
	var b strings.Builder
	for _, name := range names {
		link, err := netlink.LinkByName(name)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", name, err)
			continue
		}
		attrs := link.Attrs()
		fmt.Fprintf(&b, "%s: index %d mtu %d up %t carrier %t oper %s (flags %s)\n",
			name, attrs.Index, attrs.MTU, attrs.Flags&net.FlagUp != 0,
			attrs.RawFlags&unix.IFF_LOWER_UP != 0, attrs.OperState, attrs.Flags)

		filters, err := netlink.FilterList(link, netlink.MakeHandle(0xffff, 0))
		if err != nil {
			fmt.Fprintf(&b, "  listing ingress filters: %v\n", err)
			continue
		}
		if len(filters) == 0 {
			fmt.Fprintf(&b, "  no ingress filters\n")
		}
		for _, filter := range filters {
			fmt.Fprintf(&b, "  %s\n", describeFilter(filter))
		}
	}
	return b.String()
}

func describeFilter(filter netlink.Filter) string {
	desc := fmt.Sprintf("%s filter prio %d", filter.Type(), filter.Attrs().Priority)
	u32, ok := filter.(*netlink.U32)
	if !ok {
		return desc
	}
	for _, action := range u32.Actions {
		desc += " -> " + describeAction(action)
	}
	return desc
}

func describeAction(action netlink.Action) string {
	desc := action.Type()
	if mirred, ok := action.(*netlink.MirredAction); ok {
		target := fmt.Sprintf("index %d", mirred.Ifindex)
		if iface, err := net.InterfaceByIndex(mirred.Ifindex); err == nil {
			target = iface.Name
		}
		desc += "(" + target + ")"
	}
	stats := action.Attrs().Statistics
	if stats == nil {
		return desc + " (no statistics)"
	}
	if stats.Basic != nil {
		desc += fmt.Sprintf(" packets=%d bytes=%d", stats.Basic.Packets, stats.Basic.Bytes)
	}
	if stats.Queue != nil {
		desc += fmt.Sprintf(" drops=%d overlimits=%d", stats.Queue.Drops, stats.Queue.Overlimits)
	}
	return desc
}

// TestSplice_MovesFramesBothWays is the end-to-end check that the splice
// actually carries traffic, rather than only that the netlink objects
// were accepted: a frame put on one end of a veth pair comes out of the
// tap, and a frame written into the tap comes out of the veth.
//
// This is the whole mechanism flat mode rests on, so it is worth testing
// against the real kernel rather than a fake.
func TestSplice_MovesFramesBothWays(t *testing.T) {
	requireRoot(t)

	const (
		vethName = "splice-vm"
		peerName = "splice-net"
		tapName  = "splice-tap"
	)

	t.Cleanup(func() {
		for _, n := range []string{vethName, tapName} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	// The veth pair stands in for the container's own interface: the
	// peer is "the network", and vethName is what the guest takes over.
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: vethName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("creating veth pair: %v", err)
	}
	vethLink, err := netlink.LinkByName(vethName)
	if err != nil {
		t.Fatalf("looking up %s: %v", vethName, err)
	}
	peerLink, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatalf("looking up %s: %v", peerName, err)
	}

	tapLink, err := ensureTapMTU(tapName, vethLink.Attrs().MTU, 0, 0)
	if err != nil {
		t.Fatalf("ensureTapMTU: %v", err)
	}
	for _, l := range []netlink.Link{vethLink, peerLink} {
		if err := netlink.LinkSetUp(l); err != nil {
			t.Fatalf("bringing up %s: %v", l.Attrs().Name, err)
		}
	}

	if err := splice(vethLink, tapLink); err != nil {
		t.Fatalf("splice: %v", err)
	}

	tapFD := openTapFD(t, tapName)
	peerFD := openPacketSocket(t, peerLink.Attrs().Index)

	// Only now can readiness be judged: holding the tap open is what
	// gives it a carrier, and until it has one every frame redirected
	// into it is dropped.
	waitSpliceReady(t, []splicedPair{
		{from: vethName, to: tapName},
		{from: tapName, to: vethName},
	}, []string{peerName})

	// Peer -> tap: a frame sent into the network side should be stolen
	// off the veth's ingress and handed to the tap.
	inbound := testFrame("splice-inbound")
	sendInbound := func() error {
		_, err := unix.Write(peerFD, inbound)
		return err
	}
	if ok, obs := awaitFrame(tapFD, sendInbound, []byte("splice-inbound")); !ok {
		t.Errorf("frame sent on %s never arrived on tap %s: %s\n%s",
			peerName, tapName, obs, describeLinks(peerName, vethName, tapName))
	}

	// Tap -> peer: a frame written by the VMM should be stolen off the
	// tap's ingress and transmitted out the veth.
	outbound := testFrame("splice-outbound")
	sendOutbound := func() error {
		_, err := unix.Write(tapFD, outbound)
		return err
	}
	if ok, obs := awaitFrame(peerFD, sendOutbound, []byte("splice-outbound")); !ok {
		t.Errorf("frame written to tap %s never arrived on %s: %s\n%s",
			tapName, peerName, obs, describeLinks(tapName, vethName, peerName))
	}
}

// TestSplice_Idempotent confirms a rerun leaves one filter per direction
// rather than stacking a second copy, the same convergence Setup
// promises a retried init container.
func TestSplice_Idempotent(t *testing.T) {
	requireRoot(t)

	const (
		aName = "idem-a"
		bName = "idem-b"
	)

	t.Cleanup(func() {
		for _, n := range []string{aName, bName} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	for _, n := range []string{aName, bName} {
		if _, err := ensureTapMTU(n, 1500, 0, 0); err != nil {
			t.Fatalf("ensureTapMTU %s: %v", n, err)
		}
	}
	a, err := netlink.LinkByName(aName)
	if err != nil {
		t.Fatalf("looking up %s: %v", aName, err)
	}
	b, err := netlink.LinkByName(bName)
	if err != nil {
		t.Fatalf("looking up %s: %v", bName, err)
	}

	for i := 0; i < 2; i++ {
		if err := splice(a, b); err != nil {
			t.Fatalf("splice (run %d): %v", i+1, err)
		}
	}

	for _, l := range []netlink.Link{a, b} {
		filters, err := netlink.FilterList(l, netlink.MakeHandle(0xffff, 0))
		if err != nil {
			t.Fatalf("listing filters on %s: %v", l.Attrs().Name, err)
		}
		if len(filters) != 1 {
			t.Errorf("%s has %d filters after two splices, want 1", l.Attrs().Name, len(filters))
		}
	}
}
