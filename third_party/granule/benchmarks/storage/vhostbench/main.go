// Command vhostbench measures a vhost-user-blk backend without a VM.
//
//	vhostbench mkdata -flat disk.img -dir disk.extents -size 4G -extent 16M -zero-every 4
//	vhostbench serve  -dir disk.extents [-base base.extents] -socket s.sock -queues 4 [-cpuprofile f]
//	vhostbench run    -socket s.sock -queues 4 -qd 32 -bs 4096 -dur 10s -span 4G [-pid N]
//
// serve runs pkg/extents' server the way granule-shim does; run is a
// virtio-blk driver that talks to any vhost-user-blk socket (the Go server, or
// experimental/vhost-user-extents-rs) and prints one JSON line of results.
// See benchmarks/storage/results/vhost-user-random-read.md.
package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwsalmon/granule/pkg/extents"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: vhostbench mkdata|serve|run [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "mkdata":
		err = mkdata(os.Args[2:])
	case "serve":
		err = serve(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func parseSize(s string) (int64, error) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"), strings.HasSuffix(s, "k"):
		mult, s = 1<<10, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n * mult, err
}

// mkdata writes a flat image of random data in which every zeroEvery-th
// extent is left as a hole, then splits it into an extent directory, so reads
// cover both materialized and absent (all-zero) extents.
func mkdata(args []string) error {
	fs := flag.NewFlagSet("mkdata", flag.ExitOnError)
	flat := fs.String("flat", "", "flat image to write")
	dir := fs.String("dir", "", "extent directory to create from it")
	sizeS := fs.String("size", "4G", "virtual size")
	extS := fs.String("extent", "16M", "extent size")
	zeroEvery := fs.Int("zero-every", 0, "leave every Nth extent unallocated (0: none)")
	_ = fs.Parse(args)
	size, err := parseSize(*sizeS)
	if err != nil {
		return err
	}
	ext, err := parseSize(*extS)
	if err != nil {
		return err
	}
	f, err := os.Create(*flat)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for off := int64(0); off < size; off += ext {
		if *zeroEvery > 0 && (off/ext)%int64(*zeroEvery) == int64(*zeroEvery-1) {
			continue
		}
		for o := off; o < off+ext && o < size; o += int64(len(buf)) {
			if _, err := io.ReadFull(rand.Reader, buf); err != nil {
				return err
			}
			if _, err := f.WriteAt(buf, o); err != nil {
				return err
			}
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if *dir != "" {
		_, err = extents.SplitFromFlat(*flat, *dir, uint64(ext))
	}
	return err
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := fs.String("dir", "", "extent directory (the overlay, or the image itself)")
	base := fs.String("base", "", "optional read-only base extent directory or flat image")
	sock := fs.String("socket", "", "socket path to listen on")
	queues := fs.Int("queues", 4, "number of virtqueues")
	readOnly := fs.Bool("readonly", false, "open the store read-only")
	workers := fs.Int("workers", 0, "requests served at once per queue (0: extents.DefaultWorkersPerQueue, 1: serial)")
	cpuProf := fs.String("cpuprofile", "", "write a CPU profile here on SIGTERM")
	blockProf := fs.String("blockprofile", "", "write a block profile here on SIGTERM")
	mutexProf := fs.String("mutexprofile", "", "write a mutex profile here on SIGTERM")
	_ = fs.Parse(args)

	st, err := extents.Open(extents.Options{Dir: *dir, BasePath: *base, ReadOnly: *readOnly})
	if err != nil {
		return err
	}
	defer st.Close()
	if *cpuProf != "" {
		f, err := os.Create(*cpuProf)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
	}
	if *blockProf != "" {
		runtime.SetBlockProfileRate(1000)
	}
	if *mutexProf != "" {
		runtime.SetMutexProfileFraction(5)
	}
	_ = os.Remove(*sock)
	srv, err := extents.NewServer(st, *sock, *queues)
	if err != nil {
		return err
	}
	srv.SetWorkersPerQueue(*workers)
	srv.Start()
	fmt.Println("ready")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	if *cpuProf != "" {
		pprof.StopCPUProfile()
	}
	for name, path := range map[string]string{"block": *blockProf, "mutex": *mutexProf} {
		if path == "" {
			continue
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		_ = pprof.Lookup(name).WriteTo(f, 0)
		_ = f.Close()
	}
	return srv.Close()
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var cfg driverConfig
	fs.StringVar(&cfg.socket, "socket", "", "vhost-user-blk socket")
	fs.IntVar(&cfg.queues, "queues", 1, "virtqueues to drive (fio numjobs, one per vCPU)")
	fs.IntVar(&cfg.qd, "qd", 1, "requests in flight per queue (fio iodepth)")
	fs.IntVar(&cfg.bs, "bs", 4096, "request size")
	fs.IntVar(&cfg.seg, "seg", 4096, "bytes per data descriptor (a guest's pages are rarely contiguous)")
	fs.DurationVar(&cfg.dur, "dur", 10*time.Second, "duration")
	fs.BoolVar(&cfg.eventIdx, "event-idx", true, "negotiate VIRTIO_RING_F_EVENT_IDX, as Linux guests do")
	spanS := fs.String("span", "1G", "bytes of the disk to read across")
	pid := fs.Int("pid", 0, "server pid, to report its CPU use")
	label := fs.String("label", "", "label echoed in the output")
	verify := fs.String("verify", "", "flat image to check every read against")
	_ = fs.Parse(args)
	if *verify != "" {
		f, err := os.Open(*verify)
		if err != nil {
			return err
		}
		defer f.Close()
		cfg.verify = f
	}
	span, err := parseSize(*spanS)
	if err != nil {
		return err
	}
	cfg.span = span
	var cpu0 float64
	if *pid > 0 {
		cpu0, _ = procCPU(*pid)
	}
	res, err := runDriver(cfg)
	if err != nil {
		return err
	}
	if *pid > 0 {
		cpu1, _ := procCPU(*pid)
		res.ServerCPU = 100 * (cpu1 - cpu0) / res.Seconds
	}
	out := map[string]any{"label": *label, "queues": cfg.queues, "qd": cfg.qd, "bs": cfg.bs, "result": res}
	return json.NewEncoder(os.Stdout).Encode(out)
}
