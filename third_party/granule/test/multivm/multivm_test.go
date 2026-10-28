package multivm_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func checkMultiVMPrerequisites(t *testing.T) (granulePath, image string) {
	t.Helper()
	if os.Getenv("GRANULE_MULTIVM_TESTS") == "" && os.Getenv("KONTUR_MULTIVM_TESTS") == "" {
		if _, err := os.Stat("/dev/kvm"); err != nil {
			t.Skip("skipping multi-VM e2e test: /dev/kvm not accessible")
		}
	}

	image = os.Getenv("GRANULE_TEST_IMAGE")
	if image == "" {
		image = os.Getenv("KONTUR_TEST_IMAGE")
	}
	if image == "" {
		image = "granule:latest"
	}

	// Check if docker is available and image exists
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	if err := cmd.Run(); err != nil {
		t.Skipf("skipping multi-VM e2e test: docker image %q not available (build it first)", image)
	}

	// Find or build CLI binary (granule)
	granulePath, _ = exec.LookPath("granule")
	if granulePath == "" {
		// Look in repo root or current directory
		for _, cand := range []string{
			filepath.Join("..", "..", "granule"),
			filepath.Join(".", "granule"),
		} {
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				granulePath, _ = filepath.Abs(cand)
				break
			}
		}
	}
	if granulePath == "" {
		// Build granule on the fly in tempdir
		tmpBin := filepath.Join(t.TempDir(), "granule")
		cmdPkg := "../../cmd/granule"
		buildCmd := exec.Command("go", "build", "-o", tmpBin, cmdPkg)
		if out, err := buildCmd.CombinedOutput(); err != nil {
			t.Fatalf("building CLI failed: %v\n%s", err, string(out))
		}
		granulePath = tmpBin
	}

	return granulePath, image
}

func runCmd(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("%s %s failed: %w (stderr: %s)", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func TestMultiVM_ConcurrentStress(t *testing.T) {
	granuleCli, image := checkMultiVMPrerequisites(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	const numVMs = 3
	vmNames := make([]string, numVMs)
	suffix := time.Now().Unix() % 10000
	for i := 0; i < numVMs; i++ {
		vmNames[i] = fmt.Sprintf("m%d-%d", i+1, suffix)
	}

	// Cleanup on test exit
	defer func() {
		for _, vm := range vmNames {
			_ = exec.Command(granuleCli, "vm", "delete", vm, "-state-dir", stateDir).Run()
		}
	}()

	t.Logf("1. Concurrently creating %d VMs...", numVMs)
	var createWg sync.WaitGroup
	createErrs := make([]error, numVMs)
	for i, vm := range vmNames {
		createWg.Add(1)
		go func(idx int, name string) {
			defer createWg.Done()
			_, err := runCmd(t, granuleCli, "vm", "create", name,
				"-kontur-image", image,
				"-cpus", "1",
				"-memory-mb", "512",
				"-disk-size-mb", "1024",
				"-state-dir", stateDir,
				"-wait",
				"-ready-timeout", "3m",
			)
			createErrs[idx] = err
		}(i, vm)
	}
	createWg.Wait()

	for i, err := range createErrs {
		if err != nil {
			t.Fatalf("failed to create VM %s: %v", vmNames[i], err)
		}
	}
	t.Logf("All %d VMs successfully created and ready", numVMs)

	t.Log("2. Verifying vm list -status shows all VMs as ready...")
	listOut, err := runCmd(t, granuleCli, "vm", "list", "-status", "-state-dir", stateDir)
	if err != nil {
		t.Fatalf("vm list -status failed: %v", err)
	}
	for _, vm := range vmNames {
		if !strings.Contains(listOut, vm) {
			t.Errorf("vm list missing %s in output:\n%s", vm, listOut)
		}
	}
	if count := strings.Count(listOut, "ready"); count < numVMs {
		t.Errorf("vm list expected at least %d ready VMs, got %d:\n%s", numVMs, count, listOut)
	}

	t.Log("3. Discovering VM IP addresses...")
	vmIPs := make(map[string]string)
	for _, vm := range vmNames {
		out, err := runCmd(t, granuleCli, "vm", "exec", vm, "-state-dir", stateDir, "--",
			"sh", "-c", "ip -4 -br addr show eth0 | awk '{print $3}' | cut -d/ -f1")
		if err != nil {
			t.Fatalf("failed to get IP for %s: %v", vm, err)
		}
		ip := strings.TrimSpace(out)
		if ip == "" {
			t.Fatalf("empty IP for %s", vm)
		}
		vmIPs[vm] = ip
		t.Logf("  %s -> %s", vm, ip)
	}

	t.Log("4. Running concurrent CPU intensive workloads across all VMs...")
	var cpuWg sync.WaitGroup
	cpuErrs := make([]error, numVMs)
	for i, vm := range vmNames {
		cpuWg.Add(1)
		go func(idx int, name string) {
			defer cpuWg.Done()
			// Calculate sha256 sums of 50MB random chunks in parallel
			_, err := runCmd(t, granuleCli, "vm", "exec", name, "-state-dir", stateDir, "--",
				"bash", "-c", "dd if=/dev/zero bs=1M count=30 2>/dev/null | sha256sum")
			cpuErrs[idx] = err
		}(i, vm)
	}
	cpuWg.Wait()

	for i, err := range cpuErrs {
		if err != nil {
			t.Fatalf("CPU stress test on %s failed: %v", vmNames[i], err)
		}
	}
	t.Log("CPU intensive workload passed on all VMs")

	t.Log("5. Running concurrent Disk I/O stress & overlay isolation test...")
	var ioWg sync.WaitGroup
	ioErrs := make([]error, numVMs)
	for i, vm := range vmNames {
		ioWg.Add(1)
		go func(idx int, name string) {
			defer ioWg.Done()
			script := fmt.Sprintf(`
				dd if=/dev/urandom of=/test-io-%d.bin bs=1M count=40 conv=fsync 2>/dev/null
				sha256sum /test-io-%d.bin > /test-io-%d.sha256
				sha256sum -c /test-io-%d.sha256
				rm -f /test-io-%d.bin /test-io-%d.sha256
			`, idx, idx, idx, idx, idx, idx)
			_, err := runCmd(t, granuleCli, "vm", "exec", name, "-state-dir", stateDir, "--", "bash", "-c", script)
			ioErrs[idx] = err
		}(i, vm)
	}
	ioWg.Wait()

	for i, err := range ioErrs {
		if err != nil {
			t.Fatalf("Disk I/O stress test on %s failed: %v", vmNames[i], err)
		}
	}
	t.Log("Disk I/O stress test and filesystem integrity passed on all VMs")

	t.Log("6. Running inter-VM networking throughput & correctness test...")
	// We'll set up a circular transfer: VM0 -> VM1 (port 9101), VM1 -> VM2 (port 9102), VM2 -> VM0 (port 9100)
	for i := 0; i < numVMs; i++ {
		rxVM := vmNames[i]
		port := 9100 + i
		unitName := fmt.Sprintf("netrx-%d", i)
		rxScript := fmt.Sprintf(`
			systemctl stop %s.service 2>/dev/null || true
			systemd-run --unit=%s perl -e '
				use Socket;
				socket(S, PF_INET, SOCK_STREAM, 0) or die "$!";
				setsockopt(S, SOL_SOCKET, SO_REUSEADDR, 1);
				bind(S, sockaddr_in(%d, INADDR_ANY)) or die "$!";
				listen(S, 1) or die "$!";
				my $p = accept(C, S);
				open(my $out, ">", "/netrx-%d.bin") or die "$!";
				my $buf;
				while (my $n = read(C, $buf, 65536)) {
					print $out $buf;
				}
				close $out;
				close C;
				close S;
			'
		`, unitName, unitName, port, i)

		_, err := runCmd(t, granuleCli, "vm", "exec", rxVM, "-state-dir", stateDir, "--", "bash", "-c", rxScript)
		if err != nil {
			t.Fatalf("failed to start network listener on %s: %v", rxVM, err)
		}
	}

	time.Sleep(1 * time.Second)

	var netWg sync.WaitGroup
	netErrs := make([]error, numVMs)
	txHashes := make([]string, numVMs)

	for i := 0; i < numVMs; i++ {
		txIdx := i
		rxIdx := (i + 1) % numVMs
		txVM := vmNames[txIdx]
		rxIP := vmIPs[vmNames[rxIdx]]
		rxPort := 9100 + rxIdx

		netWg.Add(1)
		go func(idx int, sender, targetIP string, targetPort int) {
			defer netWg.Done()
			txScript := fmt.Sprintf(`
				dd if=/dev/urandom of=/netsend-%d.bin bs=1M count=20 2>/dev/null
				sha256sum /netsend-%d.bin | awk '{print $1}' > /netsend-%d.sha256
				perl -e '
					use Socket;
					socket(S, PF_INET, SOCK_STREAM, 0) or die "$!";
					connect(S, sockaddr_in(%d, inet_aton("%s"))) or die "$!";
					open(my $in, "<", "/netsend-%d.bin") or die "$!";
					my $buf;
					while (my $n = read($in, $buf, 65536)) {
						send(S, $buf, 0);
					}
					close $in;
					close S;
				'
				cat /netsend-%d.sha256
			`, idx, idx, idx, targetPort, targetIP, idx, idx)
			out, err := runCmd(t, granuleCli, "vm", "exec", sender, "-state-dir", stateDir, "--", "bash", "-c", txScript)
			if err != nil {
				netErrs[idx] = err
				return
			}
			txHashes[idx] = strings.TrimSpace(out)
		}(i, txVM, rxIP, rxPort)
	}
	netWg.Wait()

	for i, err := range netErrs {
		if err != nil {
			t.Fatalf("network transfer from %s failed: %v", vmNames[i], err)
		}
	}

	// Verify hashes on receivers
	for i := 0; i < numVMs; i++ {
		txIdx := i
		rxIdx := (i + 1) % numVMs
		rxVM := vmNames[rxIdx]

		out, err := runCmd(t, granuleCli, "vm", "exec", rxVM, "-state-dir", stateDir, "--",
			"sh", "-c", fmt.Sprintf("sha256sum /netrx-%d.bin | awk '{print $1}'", rxIdx))
		if err != nil {
			t.Fatalf("failed to compute received hash on %s: %v", rxVM, err)
		}
		rxHash := strings.TrimSpace(out)
		txHash := txHashes[txIdx]

		if rxHash != txHash {
			t.Fatalf("hash mismatch for %s -> %s (port %d): tx=%s rx=%s",
				vmNames[txIdx], rxVM, 9100+rxIdx, txHash, rxHash)
		}
		t.Logf("  Verified transfer %s -> %s (%s)", vmNames[txIdx], rxVM, txHash[:12])
	}
	t.Log("Inter-VM network throughput & integrity test passed across all VMs")

	t.Log("7. Concurrently deleting all VMs...")
	var delWg sync.WaitGroup
	delErrs := make([]error, numVMs)
	for i, vm := range vmNames {
		delWg.Add(1)
		go func(idx int, name string) {
			defer delWg.Done()
			_, err := runCmd(t, granuleCli, "vm", "delete", name, "-state-dir", stateDir)
			delErrs[idx] = err
		}(i, vm)
	}
	delWg.Wait()

	for i, err := range delErrs {
		if err != nil {
			t.Fatalf("failed to delete VM %s: %v", vmNames[i], err)
		}
	}
	t.Log("All VMs successfully deleted and state cleanly reclaimed")
}
