package hypervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// APIClient talks to the cloud-hypervisor HTTP API exposed over the local
// unix socket given to --api-socket.
type APIClient struct {
	httpClient *http.Client
}

// NewAPIClient returns a client bound to the unix socket at socketPath. The
// socket does not need to exist yet: dialing is deferred to the first
// request.
func NewAPIClient(socketPath string) *APIClient {
	dialer := net.Dialer{}
	return &APIClient{
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// PowerButton simulates an ACPI power button press in the guest, asking it
// to shut down cleanly. cloud-hypervisor exits on its own once the guest
// powers off (unless started with --no-shutdown, which this runtime never
// sets).
func (c *APIClient) PowerButton(ctx context.Context) error {
	return c.put(ctx, "/api/v1/vm.power-button")
}

// ShutdownVMM forcefully stops the VMM process without waiting on the
// guest. Used as a fallback when the guest does not respond to the power
// button in time.
func (c *APIClient) ShutdownVMM(ctx context.Context) error {
	return c.put(ctx, "/api/v1/vmm.shutdown")
}

// Pause suspends every vCPU. cloud-hypervisor requires this before
// Snapshot, and it's also the "suspend" half of Runner.Suspend.
func (c *APIClient) Pause(ctx context.Context) error {
	return c.put(ctx, "/api/v1/vm.pause")
}

// Resume unpauses a VM previously paused by Pause, or restored (with
// resume left off) from a snapshot via BuildArgs's "--restore".
func (c *APIClient) Resume(ctx context.Context) error {
	return c.put(ctx, "/api/v1/vm.resume")
}

// Snapshot writes a full snapshot of the VM's state -- enough to restore
// an identical VM later via "--restore", see BuildArgs -- to destDir.
// The VM must already be paused (see Pause), and cloud-hypervisor
// requires destDir to already exist.
func (c *APIClient) Snapshot(ctx context.Context, destDir string) error {
	return c.putJSON(ctx, "/api/v1/vm.snapshot", struct {
		DestinationURL string `json:"destination_url"`
	}{DestinationURL: "file://" + destDir})
}

// Resize asks cloud-hypervisor to live-resize the guest's RAM to
// desiredRAMBytes, via the memory hotplug device configured at boot (see
// pkg/config's CHV_MEMORY_HOTPLUG/CHV_MEMORY_MAX_MB and
// hypervisor.BuildArgs). desiredRAMBytes must fall between the VM's
// starting size (CHV_MEMORY_MB) and its ceiling (CHV_MEMORY_MAX_MB);
// cloud-hypervisor rejects the request otherwise, or if hotplug wasn't
// enabled at boot at all. The resize itself is asynchronous from the
// guest's point of view -- a virtio-mem-aware kernel picks up the change
// on its own, with no guarantee of exactly when. See the README's
// "Memory hotplug" section for how this interacts with Snapshot/suspend.
//
// Asking for the size the VM already has is an error rather than a
// no-op, because the virtio-mem device rejects a request identical to
// the one it is already holding; ResizeIfNeeded is the same call with
// that case skipped instead.
func (c *APIClient) Resize(ctx context.Context, desiredRAMBytes uint64) error {
	return c.putJSON(ctx, "/api/v1/vm.resize", struct {
		DesiredRAM uint64 `json:"desired_ram"`
	}{DesiredRAM: desiredRAMBytes})
}

// ResizeCPUs asks cloud-hypervisor to live-resize the guest's vCPU count
// to desiredVCPUs, via the ACPI CPU hotplug headroom configured at boot
// (see pkg/config's CHV_CPUS_MAX and hypervisor.BuildArgs/cpusArg).
// desiredVCPUs must fall between 1 and the VM's ceiling (CHV_CPUS_MAX,
// which defaults to CHV_CPUS itself, i.e. no headroom); cloud-hypervisor
// rejects the request otherwise.
//
// Both directions are asynchronous from the guest's point of view, same
// as memory hotplug -- but unlike virtio-mem, newly added vCPUs are not
// auto-onlined: a guest kernel must online them itself (e.g. `echo 1 >
// /sys/devices/system/cpu/cpuN/online`) before it will actually use
// them. Removal is guest-driven too (no online/offline command needed),
// but only takes effect once the guest acknowledges the ACPI eject; a
// second resize call made before that finishes is rejected by
// cloud-hypervisor with 429 ("a cpu removal is still pending"), which
// surfaces here as a plain error -- wait for one shrink to complete
// rather than retrying blindly. See the README's "CPU hotplug" section
// for how this interacts with Snapshot/suspend.
//
// Unlike Resize, asking for the count the VM already has is accepted
// silently; ResizeCPUsIfNeeded skips it anyway so that a caller can
// tell it apart from a resize that did something.
func (c *APIClient) ResizeCPUs(ctx context.Context, desiredVCPUs uint32) error {
	return c.putJSON(ctx, "/api/v1/vm.resize", struct {
		DesiredVCPUs uint32 `json:"desired_vcpus"`
	}{DesiredVCPUs: desiredVCPUs})
}

// VMInfo is cloud-hypervisor's answer to GET /api/v1/vm.info, cut down
// to the fields this runtime reads: the VM's current sizing, which is
// what makes Resize/ResizeCPUs idempotent (see ResizeIfNeeded and
// ResizeCPUsIfNeeded). The field names below were checked against a real
// cloud-hypervisor v53.0 -- the version the Dockerfile pins -- resized
// over its own API under KVM, not against its docs.
type VMInfo struct {
	Config VMConfigInfo `json:"config"`
	// State is cloud-hypervisor's own VM state name, e.g. "Running" or
	// "Paused".
	State string `json:"state"`
	// MemoryActualSize is how much RAM the guest has actually plugged
	// in, which for virtio-mem lags DesiredRAMBytes: cloud-hypervisor
	// only asks, and a guest kernel plugs (or unplugs) when it gets
	// round to it. Not the number to compare a resize request against
	// -- see DesiredRAMBytes.
	MemoryActualSize uint64 `json:"memory_actual_size"`
}

// VMConfigInfo is the "config" object of VMInfo: the VM's configuration
// as cloud-hypervisor currently holds it, which a resize rewrites in
// place rather than leaving at the values it booted with.
type VMConfigInfo struct {
	CPUs   CPUsInfo   `json:"cpus"`
	Memory MemoryInfo `json:"memory"`
}

// CPUsInfo is the "cpus" object of VMConfigInfo (see hypervisor.cpusArg
// for the boot-time end of the same two numbers).
type CPUsInfo struct {
	// BootVCPUs is the count the VM booted with -- but a successful
	// vCPU resize overwrites it with the new count, so it doubles as
	// the current desired count. See DesiredVCPUs.
	BootVCPUs uint32 `json:"boot_vcpus"`
	// MaxVCPUs is the ceiling ResizeCPUs may grow to (CHV_CPUS_MAX).
	MaxVCPUs uint32 `json:"max_vcpus"`
}

// MemoryInfo is the "memory" object of VMConfigInfo (see
// hypervisor.memoryArg for the boot-time end of the same numbers).
type MemoryInfo struct {
	// Size is the VM's starting size in bytes (CHV_MEMORY_MB). Unlike
	// BootVCPUs this is *not* rewritten by a resize: virtio-mem growth
	// is reported in HotpluggedSize instead.
	Size uint64 `json:"size"`
	// HotplugSize is the headroom the virtio-mem device was given at
	// boot, i.e. CHV_MEMORY_MAX_MB minus CHV_MEMORY_MB. Zero when the
	// VM booted without memory hotplug.
	HotplugSize uint64 `json:"hotplug_size"`
	// HotpluggedSize is how much of that headroom the last successful
	// Resize asked for. This is exactly the "requested_size" the
	// virtio-mem device compares a new request against -- asking for
	// the size it already holds is the 500 that ResizeIfNeeded exists
	// to avoid. cloud-hypervisor omits the field entirely while it is
	// zero, which unmarshals to the same 0.
	HotpluggedSize uint64 `json:"hotplugged_size"`
}

// DesiredRAMBytes is the guest RAM size the VM is currently sized to,
// in the same units Resize takes: its starting size plus whatever the
// last Resize hotplugged on top. The guest may not have caught up yet
// (see MemoryActualSize), but this is the number cloud-hypervisor
// compares a fresh Resize against.
func (i VMInfo) DesiredRAMBytes() uint64 {
	return i.Config.Memory.Size + i.Config.Memory.HotpluggedSize
}

// DesiredVCPUs is the vCPU count the VM is currently sized to, in the
// same units ResizeCPUs takes. As with DesiredRAMBytes the guest may
// not have caught up: added vCPUs still need onlining, and removed ones
// leave once the guest acknowledges the eject.
func (i VMInfo) DesiredVCPUs() uint32 {
	return i.Config.CPUs.BootVCPUs
}

// Info reports how the VM is currently configured, per cloud-hypervisor's
// GET /api/v1/vm.info. Mainly useful for the sizes a resize has to be
// compared against; see VMInfo.
func (c *APIClient) Info(ctx context.Context) (VMInfo, error) {
	body, err := c.doBody(ctx, http.MethodGet, "/api/v1/vm.info", nil)
	if err != nil {
		return VMInfo{}, err
	}
	var info VMInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return VMInfo{}, fmt.Errorf("decoding cloud-hypervisor api GET /api/v1/vm.info response: %w", err)
	}
	return info, nil
}

// ResizeIfNeeded is Resize, minus the request cloud-hypervisor would
// reject for asking for nothing: it reads the VM's current size (see
// Info) first and reports resized=false, having sent no resize at all,
// when the guest is already sized to desiredRAMBytes. The virtio-mem
// device refuses a request for the size it already holds with a 500
// ("new size 0x... and requested_size are identical"), which makes an
// otherwise fine "resize to exactly this much" script fail the second
// time it runs; callers that want that raw behaviour can still use
// Resize.
//
// A VM whose guest has not finished plugging in the memory of an
// earlier resize still counts as already sized: what is compared is
// what cloud-hypervisor was last asked for, which is also what it
// compares against.
func (c *APIClient) ResizeIfNeeded(ctx context.Context, desiredRAMBytes uint64) (resized bool, err error) {
	info, err := c.Info(ctx)
	if err != nil {
		return false, err
	}
	if info.DesiredRAMBytes() == desiredRAMBytes {
		return false, nil
	}
	if err := c.Resize(ctx, desiredRAMBytes); err != nil {
		return false, err
	}
	return true, nil
}

// ResizeCPUsIfNeeded is ResizeCPUs with the same short-circuit
// ResizeIfNeeded applies to memory: it reports resized=false, having
// sent nothing, when the VM is already sized to desiredVCPUs.
// cloud-hypervisor accepts a vCPU resize to the count it already has
// silently rather than failing it, so this changes no outcome -- it is
// here so that "granule resize" can say the same thing about both halves
// instead of only noticing the no-op on one of them.
func (c *APIClient) ResizeCPUsIfNeeded(ctx context.Context, desiredVCPUs uint32) (resized bool, err error) {
	info, err := c.Info(ctx)
	if err != nil {
		return false, err
	}
	if info.DesiredVCPUs() == desiredVCPUs {
		return false, nil
	}
	if err := c.ResizeCPUs(ctx, desiredVCPUs); err != nil {
		return false, err
	}
	return true, nil
}

// WaitReady polls the API socket until it accepts connections or ctx is
// done. cloud-hypervisor creates the socket very early in startup, but the
// runtime may race it right after Start returns.
func (c *APIClient) WaitReady(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := c.get(ctx, "/api/v1/vmm.ping"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *APIClient) put(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodPut, path, nil)
}

func (c *APIClient) putJSON(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request body for %s %s: %w", http.MethodPut, path, err)
	}
	return c.do(ctx, http.MethodPut, path, bytes.NewReader(b))
}

func (c *APIClient) get(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *APIClient) do(ctx context.Context, method, path string, body io.Reader) error {
	_, err := c.doBody(ctx, method, path, body)
	return err
}

// doBody is do for the endpoints whose answer is worth reading: most of
// cloud-hypervisor's API replies 204 with an empty body, but vm.info
// replies with the JSON Info unmarshals.
func (c *APIClient) doBody(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cloud-hypervisor api %s %s: %s: %s", method, path, resp.Status, string(respBody))
	}
	if readErr != nil {
		return nil, fmt.Errorf("reading cloud-hypervisor api %s %s response: %w", method, path, readErr)
	}
	return respBody, nil
}
