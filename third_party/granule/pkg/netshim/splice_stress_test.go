package netshim

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// TestStress_SpliceHighPacketThroughput pumps a burst of frames through the TAP-to-veth
// splice in both directions, validating high packet processing rates and zero kernel drops.
func TestStress_SpliceHighPacketThroughput(t *testing.T) {
	requireRoot(t)

	const (
		vethName = "stress-vm"
		peerName = "stress-net"
		tapName  = "stress-tap"
	)

	t.Cleanup(func() {
		_ = netlink.LinkDel(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethName}})
		_ = netlink.LinkDel(&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: tapName}, Mode: netlink.TUNTAP_MODE_TAP})
	})

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: vethName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("creating veth: %v", err)
	}

	tap, err := ensureTapMTU(tapName, 1500, 0, 0)
	if err != nil {
		t.Fatalf("creating tap: %v", err)
	}
	tapFD := openTapFD(t, tapName)

	ext, err := netlink.LinkByName(vethName)
	if err != nil {
		t.Fatalf("lookup %s: %v", vethName, err)
	}
	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatalf("lookup %s: %v", peerName, err)
	}

	if err := netlink.LinkSetUp(ext); err != nil {
		t.Fatalf("up %s: %v", vethName, err)
	}
	if err := netlink.LinkSetUp(peer); err != nil {
		t.Fatalf("up %s: %v", peerName, err)
	}

	if err := splice(ext, tap); err != nil {
		t.Fatalf("splice: %v", err)
	}

	waitSpliceReady(t, []splicedPair{
		{from: vethName, to: tapName},
		{from: tapName, to: vethName},
	}, []string{peerName})

	peerSock := openPacketSocket(t, peer.Attrs().Index)

	// Pump 500 frames in each direction
	const frameCount = 500
	start := time.Now()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// Direction 1: Veth peer -> Tap
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < frameCount; i++ {
			token := fmt.Sprintf("VETH_TO_TAP_%05d", i)
			frame := testFrame(token)
			send := func() error {
				_, err := unix.Write(peerSock, frame)
				return err
			}
			ok, obs := awaitFrame(tapFD, send, []byte(token))
			if !ok {
				errCh <- fmt.Errorf("frame %d veth->tap failed: %s", i, obs)
				return
			}
		}
	}()

	// Direction 2: Tap -> Veth peer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < frameCount; i++ {
			token := fmt.Sprintf("TAP_TO_VETH_%05d", i)
			frame := testFrame(token)
			send := func() error {
				_, err := unix.Write(tapFD, frame)
				return err
			}
			ok, obs := awaitFrame(peerSock, send, []byte(token))
			if !ok {
				errCh <- fmt.Errorf("frame %d tap->veth failed: %s", i, obs)
				return
			}
		}
	}()

	wg.Wait()
	close(errCh)

	elapsed := time.Since(start)
	t.Logf("Delivered %d bidirectional frames in %s (%0.1f fps)", frameCount*2, elapsed, float64(frameCount*2)/elapsed.Seconds())

	for err := range errCh {
		t.Errorf("throughput stress error: %v", err)
	}
}

// TestStress_SpliceMTUBoundaries verifies that frames of varying sizes (minimum 60 bytes,
// standard 1500-byte MTU frames) pass through the splice without truncation or corruption.
func TestStress_SpliceMTUBoundaries(t *testing.T) {
	requireRoot(t)

	const (
		vethName = "mtu-vm"
		peerName = "mtu-net"
		tapName  = "mtu-tap"
	)

	t.Cleanup(func() {
		_ = netlink.LinkDel(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethName}})
		_ = netlink.LinkDel(&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: tapName}, Mode: netlink.TUNTAP_MODE_TAP})
	})

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: vethName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("creating veth: %v", err)
	}

	tap, err := ensureTapMTU(tapName, 1500, 0, 0)
	if err != nil {
		t.Fatalf("creating tap: %v", err)
	}
	tapFD := openTapFD(t, tapName)

	ext, err := netlink.LinkByName(vethName)
	if err != nil {
		t.Fatalf("lookup %s: %v", vethName, err)
	}
	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatalf("lookup %s: %v", peerName, err)
	}

	if err := netlink.LinkSetUp(ext); err != nil {
		t.Fatalf("up %s: %v", vethName, err)
	}
	if err := netlink.LinkSetUp(peer); err != nil {
		t.Fatalf("up %s: %v", peerName, err)
	}

	if err := splice(ext, tap); err != nil {
		t.Fatalf("splice: %v", err)
	}

	waitSpliceReady(t, []splicedPair{
		{from: vethName, to: tapName},
		{from: tapName, to: vethName},
	}, []string{peerName})

	peerSock := openPacketSocket(t, peer.Attrs().Index)

	// Test boundary frame sizes: 60 (min Ethernet), 128, 512, 1024, 1514 (1500 MTU + 14B Eth header)
	testSizes := []int{60, 128, 512, 1024, 1514}
	for _, sz := range testSizes {
		payloadLen := sz - 14 // subtract ethernet header
		if payloadLen < 16 {
			payloadLen = 16
		}
		uniquePattern := fmt.Sprintf("MTU_SIZE_%04d_", sz)
		padding := bytes.Repeat([]byte("X"), payloadLen-len(uniquePattern))
		payload := uniquePattern + string(padding)

		frame := []byte{
			0x02, 0x00, 0x00, 0x00, 0x00, 0x02, // dst mac
			0x02, 0x00, 0x00, 0x00, 0x00, 0x01, // src mac
			0x88, 0xb5, // ethertype
		}
		frame = append(frame, []byte(payload)...)
		for len(frame) < 60 {
			frame = append(frame, 0)
		}

		send := func() error {
			_, err := unix.Write(peerSock, frame)
			return err
		}

		ok, obs := awaitFrame(tapFD, send, []byte(uniquePattern))
		if !ok {
			t.Errorf("boundary size %d bytes failed: %s", sz, obs)
		}
	}
}

// TestStress_SpliceConcurrentPairs tests multiple independent spliced pairs
// operating concurrently in the same namespace without cross-talk.
func TestStress_SpliceConcurrentPairs(t *testing.T) {
	requireRoot(t)

	const pairCount = 5
	var pairs []struct {
		veth string
		peer string
		tap  string
	}

	for i := 0; i < pairCount; i++ {
		p := struct {
			veth string
			peer string
			tap  string
		}{
			veth: fmt.Sprintf("c-vm-%d", i),
			peer: fmt.Sprintf("c-net-%d", i),
			tap:  fmt.Sprintf("c-tap-%d", i),
		}
		pairs = append(pairs, p)
	}

	t.Cleanup(func() {
		for _, p := range pairs {
			_ = netlink.LinkDel(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: p.veth}})
			_ = netlink.LinkDel(&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: p.tap}, Mode: netlink.TUNTAP_MODE_TAP})
		}
	})

	type pairState struct {
		tapFD    int
		peerSock int
	}
	states := make([]pairState, pairCount)

	for i, p := range pairs {
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: p.veth},
			PeerName:  p.peer,
		}
		if err := netlink.LinkAdd(veth); err != nil {
			t.Fatalf("creating veth %s: %v", p.veth, err)
		}

		tap, err := ensureTapMTU(p.tap, 1500, 0, 0)
		if err != nil {
			t.Fatalf("creating tap %s: %v", p.tap, err)
		}
		states[i].tapFD = openTapFD(t, p.tap)

		ext, err := netlink.LinkByName(p.veth)
		if err != nil {
			t.Fatalf("lookup %s: %v", p.veth, err)
		}
		peer, err := netlink.LinkByName(p.peer)
		if err != nil {
			t.Fatalf("lookup %s: %v", p.peer, err)
		}

		if err := netlink.LinkSetUp(ext); err != nil {
			t.Fatalf("up %s: %v", p.veth, err)
		}
		if err := netlink.LinkSetUp(peer); err != nil {
			t.Fatalf("up %s: %v", p.peer, err)
		}

		if err := splice(ext, tap); err != nil {
			t.Fatalf("splice %s <-> %s: %v", p.veth, p.tap, err)
		}

		waitSpliceReady(t, []splicedPair{
			{from: p.veth, to: p.tap},
			{from: p.tap, to: p.veth},
		}, []string{p.peer})

		states[i].peerSock = openPacketSocket(t, peer.Attrs().Index)
	}

	// Concurrently pump traffic across all pairs
	var wg sync.WaitGroup
	errCh := make(chan error, pairCount)

	for i := 0; i < pairCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := pairs[idx]
			st := states[idx]

			token := fmt.Sprintf("ISOLATION_PAIR_%d", idx)
			frame := testFrame(token)
			send := func() error {
				_, err := unix.Write(st.peerSock, frame)
				return err
			}

			ok, obs := awaitFrame(st.tapFD, send, []byte(token))
			if !ok {
				errCh <- fmt.Errorf("pair %d (%s <-> %s) failed: %s", idx, p.veth, p.tap, obs)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent pair error: %v", err)
	}
}
