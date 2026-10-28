// Command fakechv stands in for the cloud-hypervisor binary in tests of
// the Runner's process, shutdown and suspend handling. It understands
// just enough of the real CLI and API to exercise Runner: it parses
// --api-socket, --memory and --cpus from argv, serves the same
// vm.power-button / vmm.shutdown / vm.pause / vm.resume / vm.snapshot /
// vm.resize / vm.info endpoints, and its behaviour is tuned through
// environment variables rather than flags so tests can drive it without
// inventing a parallel CLI.
//
//   - FAKECHV_SERVE_API=0   don't start the API socket at all
//   - FAKECHV_EXIT_ON_POWER_BUTTON=0   ack vm.power-button but keep running
//   - FAKECHV_EXIT_ON_VMM_SHUTDOWN=0   ack vmm.shutdown but keep running
//   - FAKECHV_IGNORE_SIGTERM=1   don't exit on SIGTERM either
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	var socketPath, memoryArg, cpusArg string
	for i, a := range os.Args {
		if i+1 >= len(os.Args) {
			continue
		}
		switch a {
		case "--api-socket":
			socketPath = strings.TrimPrefix(os.Args[i+1], "path=")
		case "--memory":
			memoryArg = os.Args[i+1]
		case "--cpus":
			cpusArg = os.Args[i+1]
		}
	}
	sizing := newSizing(memoryArg, cpusArg)

	if envBool("FAKECHV_SERVE_API", true) && socketPath != "" {
		l, err := net.Listen("unix", socketPath)
		if err != nil {
			os.Exit(2)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/vmm.ping", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/api/v1/vm.power-button", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
			if envBool("FAKECHV_EXIT_ON_POWER_BUTTON", true) {
				go delayedExit(0)
			}
		})
		mux.HandleFunc("/api/v1/vmm.shutdown", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
			if envBool("FAKECHV_EXIT_ON_VMM_SHUTDOWN", true) {
				go delayedExit(0)
			}
		})
		mux.HandleFunc("/api/v1/vm.pause", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/v1/vm.resume", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/v1/vm.snapshot", func(w http.ResponseWriter, r *http.Request) {
			// Leaves a world-readable memory-ranges behind, the way a
			// VMM running under the usual 022 umask does, so tests can
			// tell the caller tightens it rather than trusting it.
			var req struct {
				DestinationURL string `json:"destination_url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				if dir, ok := strings.CutPrefix(req.DestinationURL, "file://"); ok {
					mem := dir + "/memory-ranges"
					if os.WriteFile(mem, []byte("guest memory"), 0o644) == nil {
						os.Chmod(mem, 0o644)
					}
				}
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/v1/vm.info", sizing.serveInfo)
		mux.HandleFunc("/api/v1/vm.resize", sizing.serveResize)
		go http.Serve(l, mux)
	}

	if !envBool("FAKECHV_IGNORE_SIGTERM", false) {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		go func() {
			<-sigCh
			os.Exit(3)
		}()
	}

	select {}
}

// sizing is the VM sizing state vm.info reports and vm.resize changes.
// It reproduces what a real cloud-hypervisor v53.0 does, checked against
// that binary under KVM: a memory resize leaves the boot size alone and
// moves the virtio-mem "hotplugged_size" (which the JSON omits entirely
// while it is zero), a vCPU resize overwrites "boot_vcpus", and asking
// virtio-mem for the size it already holds is a 500 rather than a no-op
// -- the failure hypervisor.APIClient.ResizeIfNeeded exists to skip.
// The one liberty it takes is reporting memory_actual_size as the size
// asked for: a real guest plugs the memory in on its own schedule, and
// nothing here needs the lag.
type sizing struct {
	mu             sync.Mutex
	memorySize     uint64 // boot size, in bytes
	hotplugSize    uint64 // virtio-mem headroom above the boot size
	hotpluggedSize uint64 // how much of that headroom is currently asked for
	bootVCPUs      uint32 // boot count, rewritten by a vCPU resize
	maxVCPUs       uint32
}

// newSizing reads the starting sizes out of the --memory and --cpus
// values the runtime built (see hypervisor.memoryArg/cpusArg), so a
// fakechv started by Runner reports the config that Runner asked for.
func newSizing(memoryArg, cpusArg string) *sizing {
	s := &sizing{}
	for _, field := range strings.Split(memoryArg, ",") {
		k, v, _ := strings.Cut(field, "=")
		switch k {
		case "size":
			s.memorySize = parseSize(v)
		case "hotplug_size":
			s.hotplugSize = parseSize(v)
		}
	}
	for _, field := range strings.Split(cpusArg, ",") {
		k, v, _ := strings.Cut(field, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			continue
		}
		switch k {
		case "boot":
			s.bootVCPUs = uint32(n)
		case "max":
			s.maxVCPUs = uint32(n)
		}
	}
	if s.maxVCPUs < s.bootVCPUs {
		s.maxVCPUs = s.bootVCPUs
	}
	return s
}

func (s *sizing) serveInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	memory := map[string]any{
		"size":           s.memorySize,
		"hotplug_method": "VirtioMem",
		"shared":         true,
	}
	if s.hotplugSize > 0 {
		memory["hotplug_size"] = s.hotplugSize
	}
	if s.hotpluggedSize > 0 {
		memory["hotplugged_size"] = s.hotpluggedSize
	}
	info := map[string]any{
		"config": map[string]any{
			"cpus": map[string]any{
				"boot_vcpus": s.bootVCPUs,
				"max_vcpus":  s.maxVCPUs,
			},
			"memory": memory,
		},
		"state":              "Running",
		"memory_actual_size": s.memorySize + s.hotpluggedSize,
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *sizing) serveResize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DesiredRAM   uint64 `json:"desired_ram"`
		DesiredVCPUs uint32 `json:"desired_vcpus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if req.DesiredRAM > 0 {
		if req.DesiredRAM < s.memorySize {
			http.Error(w, "Invalid resize request: desired_size below boot size", http.StatusInternalServerError)
			return
		}
		if req.DesiredRAM > s.memorySize+s.hotplugSize {
			http.Error(w, "Failed to update virtio configuration: ResizeError(new size is bigger than region_size)", http.StatusInternalServerError)
			return
		}
		hotplugged := req.DesiredRAM - s.memorySize
		if hotplugged == s.hotpluggedSize {
			http.Error(w, "Failed to update virtio configuration: ResizeError(new size 0x"+strconv.FormatUint(hotplugged, 16)+" and requested_size are identical)", http.StatusInternalServerError)
			return
		}
		s.hotpluggedSize = hotplugged
	}
	if req.DesiredVCPUs > 0 {
		if req.DesiredVCPUs > s.maxVCPUs {
			http.Error(w, "Requested vCPUs exceed maximum", http.StatusInternalServerError)
			return
		}
		// A repeat of the count the VM already has is accepted
		// silently, same as the real thing.
		s.bootVCPUs = req.DesiredVCPUs
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseSize reads cloud-hypervisor's size syntax -- a byte count with an
// optional K/M/G suffix -- as used in --memory's "size=" and
// "hotplug_size=".
func parseSize(v string) uint64 {
	mult := uint64(1)
	if len(v) > 0 {
		switch v[len(v)-1] {
		case 'K', 'k':
			mult, v = 1024, v[:len(v)-1]
		case 'M', 'm':
			mult, v = 1024*1024, v[:len(v)-1]
		case 'G', 'g':
			mult, v = 1024*1024*1024, v[:len(v)-1]
		}
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	return n * mult
}

func delayedExit(code int) {
	time.Sleep(10 * time.Millisecond)
	os.Exit(code)
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
