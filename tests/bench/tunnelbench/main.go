// Command tunnelbench measures the carriers a sandbox's control-plane traffic
// can take to the controller: the direct bridge route, and byte-stream
// tunnels (docker exec -i, kubectl exec -i, kubectl port-forward) carrying
// pkg/tunnel/mux. See scripts/bench/README.md for how the pieces fit and
// docs/test-runs/2026-10-24-tunnel-vs-direct.md for what it measured.
//
// Subcommands:
//
//	serve    controller side: the endpoints the workloads hit
//	forward  controller side: runs a stdio carrier (or dials a port-forward)
//	         and splices every mux stream the sandbox opens to -target
//	agent    sandbox side: speaks mux on stdin/stdout (or on one accepted
//	         TCP connection) and turns each local TCP connection into a stream
//	load     sandbox side: drives one workload and prints one JSON result line
//	cpu      prints the CPU seconds of processes by command name
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bwsalmon/grain/pkg/tunnel/mux"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tunnelbench serve|forward|agent|load|cpu [flags]")
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "serve":
		serve(args)
	case "forward":
		forward(args)
	case "agent":
		agent(args)
	case "load":
		load(args)
	case "cpu":
		cpu(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

// --- controller side: the endpoints ---

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":4295", "listen address")
	gitRoot := fs.String("git-root", "", "directory of bare repositories served at /git/ by git http-backend")
	files := fs.String("files", "", "directory served at /files/ (a GOPROXY tree, for one)")
	_ = fs.Parse(args)

	h := http.NewServeMux()
	h.HandleFunc("/rpc", handleRPC)
	h.HandleFunc("/bulk", handleBulk)
	h.HandleFunc("/sse", handleSSE)
	h.HandleFunc("/rusage", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%.3f\n", selfCPU())
	})
	if *files != "" {
		h.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(*files))))
	}
	if *gitRoot != "" {
		gitPath, err := exec.LookPath("git")
		if err != nil {
			log.Fatal(err)
		}
		h.Handle("/git/", &cgi.Handler{
			Path: gitPath,
			Args: []string{"http-backend"},
			Env:  []string{"GIT_PROJECT_ROOT=" + *gitRoot, "GIT_HTTP_EXPORT_ALL=1"},
			Root: "/git",
		})
	}
	log.Printf("serving on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}

// handleRPC answers a JSON-RPC request the size of an MCP tools/call with a
// result the size of `run_command echo hello`'s.
func handleRPC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"exit=0\nstdout:\nhello\n\nstderr:\n"}],"isError":false}}`, req.ID)
}

var zeros = make([]byte, 256<<10)

func handleBulk(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	for n > 0 {
		chunk := int64(len(zeros))
		if n < chunk {
			chunk = n
		}
		if _, err := w.Write(zeros[:chunk]); err != nil {
			return
		}
		n -= chunk
	}
}

// handleSSE is a stub model upstream: a streamed completion of `tokens`
// chunks, each flushed on its own, with no model latency between them.
func handleSSE(w http.ResponseWriter, r *http.Request) {
	tokens, _ := strconv.Atoi(r.URL.Query().Get("tokens"))
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	for i := 0; i < tokens; i++ {
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" tok%d\"}}\n\n", i)
		if fl != nil {
			fl.Flush()
		}
	}
	fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

// --- the tunnel ---

// forward is the controller end of a tunnel. With a command it runs the
// command (docker exec -i, kubectl exec -i) and speaks mux over its stdio;
// with -dial it speaks mux over one TCP connection (kubectl port-forward's
// local port). Every stream the sandbox opens is spliced to -target.
func forward(args []string) {
	fs := flag.NewFlagSet("forward", flag.ExitOnError)
	target := fs.String("target", "127.0.0.1:4295", "controller address each stream is spliced to")
	dial := fs.String("dial", "", "speak mux over a TCP connection to this address instead of a command's stdio")
	stats := fs.String("stats", "", "serve this process's CPU (and its command's) on this address")
	_ = fs.Parse(args)

	var rwc io.ReadWriteCloser
	var child *exec.Cmd
	if *dial != "" {
		var conn net.Conn
		var err error
		for i := 0; i < 50; i++ {
			if conn, err = net.Dial("tcp", *dial); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			log.Fatal(err)
		}
		rwc = conn
	} else {
		if fs.NArg() == 0 {
			log.Fatal("forward: need -dial or a command")
		}
		child = exec.Command(fs.Arg(0), fs.Args()[1:]...)
		child.Stderr = os.Stderr
		in, err := child.StdinPipe()
		if err != nil {
			log.Fatal(err)
		}
		out, err := child.StdoutPipe()
		if err != nil {
			log.Fatal(err)
		}
		if err := child.Start(); err != nil {
			log.Fatal(err)
		}
		rwc = stdioConn{out, in}
	}
	if *stats != "" {
		go func() {
			log.Fatal(http.ListenAndServe(*stats, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				c := selfCPU()
				if child != nil && child.Process != nil {
					c += procCPU(child.Process.Pid)
				}
				fmt.Fprintf(w, "%.3f\n", c)
			})))
		}()
	}
	m := mux.NewMux(rwc, mux.Config{IsServer: true})
	start := time.Now()
	for {
		s, err := m.AcceptStream()
		if err != nil {
			log.Printf("forward: session ended after %s: %v", time.Since(start).Round(time.Millisecond), err)
			os.Exit(1)
		}
		go func() {
			c, err := net.Dial("tcp", *target)
			if err != nil {
				log.Printf("dial %s: %v", *target, err)
				_ = s.Reset()
				return
			}
			splice(c, s)
		}()
	}
}

// agent is the sandbox end of a tunnel: each TCP connection accepted on
// -listen becomes one mux stream to the controller.
func agent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:4295", "where sandbox clients connect")
	muxListen := fs.String("mux-listen", "", "accept one TCP connection here and speak mux over it instead of stdio")
	_ = fs.Parse(args)

	var rwc io.ReadWriteCloser = stdioConn{os.Stdin, os.Stdout}
	if *muxListen != "" {
		l, err := net.Listen("tcp", *muxListen)
		if err != nil {
			log.Fatal(err)
		}
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		_ = l.Close()
		rwc = conn
	}
	m := mux.NewMux(rwc, mux.Config{IsServer: false})
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		<-m.Done()
		log.Printf("agent: session ended")
		os.Exit(1)
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			s, err := m.OpenStream(context.Background())
			if err != nil {
				log.Printf("open stream: %v", err)
				_ = c.Close()
				return
			}
			splice(c, s)
		}()
	}
}

type stdioConn struct {
	io.Reader
	io.WriteCloser
}

func splice(c net.Conn, s *mux.Stream) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(s, c)
		_ = s.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, s)
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = c.Close()
	_ = s.Close()
}

// --- sandbox side: the load generator ---

type result struct {
	Carrier    string  `json:"carrier"`
	Workload   string  `json:"workload"`
	Conc       int     `json:"conc"`
	Ops        int     `json:"ops"`
	Errors     int64   `json:"errors"`
	WallS      float64 `json:"wall_s"`
	P50Ms      float64 `json:"p50_ms"`
	P95Ms      float64 `json:"p95_ms"`
	P99Ms      float64 `json:"p99_ms"`
	TTFBP50Ms  float64 `json:"ttfb_p50_ms,omitempty"`
	OpsPerS    float64 `json:"ops_per_s"`
	MiBPerS    float64 `json:"mib_per_s,omitempty"`
	Bytes      int64   `json:"bytes"`
	SandboxCPU float64 `json:"sandbox_cpu_s"`
}

func load(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	base := fs.String("url", "http://127.0.0.1:4295", "controller base URL as the sandbox reaches it")
	carrier := fs.String("carrier", "", "label for the result")
	workload := fs.String("workload", "rpc", "rpc | sse | bulk | replay | soak")
	conc := fs.Int("c", 1, "concurrent workers, each on its own connection")
	n := fs.Int("n", 200, "operations per worker (rpc, sse)")
	size := fs.Int64("size", 1<<30, "bytes per bulk stream")
	list := fs.String("list", "", "replay: file of sizes, one per line, fetched from /bulk")
	tokens := fs.Int("tokens", 2000, "sse: tokens per completion")
	dur := fs.Duration("dur", time.Hour, "soak: how long")
	interval := fs.Duration("interval", time.Second, "soak: time between calls")
	_ = fs.Parse(args)

	cpu0 := sandboxCPU()
	var lat, ttfb []time.Duration
	var mu sync.Mutex
	var bytesRead, errs int64
	record := func(d, t time.Duration, b int64, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs++
			log.Printf("%s: %v", *workload, err)
			return
		}
		lat = append(lat, d)
		if t > 0 {
			ttfb = append(ttfb, t)
		}
		bytesRead += b
	}
	newClient := func() *http.Client {
		return &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, DisableCompression: true}}
	}

	start := time.Now()
	var wg sync.WaitGroup
	switch *workload {
	case "rpc":
		for w := 0; w < *conc; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				cl := newClient()
				for i := 0; i < *n; i++ {
					body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"run_command","arguments":{"command":"echo hello","description":"bench %d"}}}`, i, w)
					t0 := time.Now()
					b, err := fetch(cl, "POST", *base+"/rpc", body)
					record(time.Since(t0), 0, b, err)
				}
			}(w)
		}
	case "sse":
		for w := 0; w < *conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cl := newClient()
				for i := 0; i < *n; i++ {
					d, t, b, err := stream(cl, fmt.Sprintf("%s/sse?tokens=%d", *base, *tokens))
					record(d, t, b, err)
				}
			}()
		}
	case "bulk":
		for w := 0; w < *conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				t0 := time.Now()
				b, err := fetch(newClient(), "GET", fmt.Sprintf("%s/bulk?n=%d", *base, *size), "")
				record(time.Since(t0), 0, b, err)
			}()
		}
	case "replay":
		// A package manager's fetch pattern: the sizes a real install
		// fetches, -c at a time, over -c keep-alive connections.
		sizes := readSizes(*list)
		next := int64(-1)
		for w := 0; w < *conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cl := newClient()
				for {
					i := atomic.AddInt64(&next, 1)
					if i >= int64(len(sizes)) {
						return
					}
					t0 := time.Now()
					b, err := fetch(cl, "GET", fmt.Sprintf("%s/bulk?n=%d", *base, sizes[i]), "")
					record(time.Since(t0), 0, b, err)
				}
			}()
		}
	case "soak":
		// One call per interval for dur; prints every failure and how long
		// until the next success, which is the reconnect time.
		cl := newClient()
		var downSince time.Time
		for time.Since(start) < *dur {
			t0 := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			b, err := fetchCtx(ctx, cl, "POST", *base+"/rpc", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
			cancel()
			record(time.Since(t0), 0, b, err)
			switch {
			case err != nil && downSince.IsZero():
				downSince = t0
				fmt.Fprintf(os.Stderr, "%s DOWN %v\n", t0.Format(time.RFC3339), err)
			case err == nil && !downSince.IsZero():
				fmt.Fprintf(os.Stderr, "%s UP after %s\n", time.Now().Format(time.RFC3339), time.Since(downSince).Round(time.Millisecond))
				downSince = time.Time{}
			}
			time.Sleep(*interval)
		}
	default:
		log.Fatalf("unknown workload %q", *workload)
	}
	wg.Wait()
	wall := time.Since(start)

	r := result{Carrier: *carrier, Workload: *workload, Conc: *conc, Ops: len(lat), Errors: errs,
		WallS: wall.Seconds(), Bytes: bytesRead, SandboxCPU: round3(sandboxCPU() - cpu0)}
	r.P50Ms, r.P95Ms, r.P99Ms = pct(lat, 50), pct(lat, 95), pct(lat, 99)
	r.TTFBP50Ms = pct(ttfb, 50)
	r.OpsPerS = round3(float64(len(lat)) / wall.Seconds())
	if *workload == "bulk" || *workload == "replay" {
		r.MiBPerS = round3(float64(bytesRead) / (1 << 20) / wall.Seconds())
	}
	r.WallS = round3(r.WallS)
	out, _ := json.Marshal(r)
	fmt.Println(string(out))
}

func fetch(cl *http.Client, method, url, body string) (int64, error) {
	return fetchCtx(context.Background(), cl, method, url, body)
}

func fetchCtx(ctx context.Context, cl *http.Client, method, url, body string) (int64, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("%s", resp.Status)
	}
	return n, err
}

// stream reads a server-sent-event response and returns its total time and
// the time to its first event.
func stream(cl *http.Client, url string) (time.Duration, time.Duration, int64, error) {
	t0 := time.Now()
	resp, err := cl.Get(url)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	var first time.Duration
	var n int64
	for {
		line, err := br.ReadBytes('\n')
		n += int64(len(line))
		if first == 0 && bytes.HasPrefix(line, []byte("data:")) {
			first = time.Since(t0)
		}
		if err == io.EOF {
			return time.Since(t0), first, n, nil
		}
		if err != nil {
			return 0, 0, n, err
		}
	}
}

func readSizes(path string) []int64 {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	var out []int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseInt(fields[0], 10, 64); err == nil && v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func pct(ds []time.Duration, p float64) float64 {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return round3(float64(s[i]) / float64(time.Millisecond))
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// --- CPU accounting ---

func selfCPU() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return tv(ru.Utime) + tv(ru.Stime)
}

func tv(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }

// procCPU is a process's user+system CPU seconds from /proc/<pid>/stat,
// assuming the usual 100 clock ticks a second.
func procCPU(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 13 {
		return 0
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / 100
}

func procComm(pid int) string {
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	return strings.TrimSpace(string(b))
}

// sandboxCPU is this process's CPU plus every other tunnelbench process
// visible to it -- inside a container or pod, that is the agent.
func sandboxCPU() float64 {
	return selfCPU() + commCPU(map[string]bool{"tunnelbench": true}, os.Getpid())
}

func commCPU(names map[string]bool, skip int) float64 {
	ents, _ := filepath.Glob("/proc/[0-9]*")
	var sum float64
	for _, e := range ents {
		pid, err := strconv.Atoi(filepath.Base(e))
		if err != nil || pid == skip {
			continue
		}
		comm := procComm(pid)
		for name := range names {
			if strings.HasPrefix(comm, name) {
				sum += procCPU(pid)
				break
			}
		}
	}
	return sum
}

func cpu(args []string) {
	fs := flag.NewFlagSet("cpu", flag.ExitOnError)
	comms := fs.String("comm", "dockerd,containerd,kubectl", "comma-separated command-name prefixes to sum")
	_ = fs.Parse(args)
	names := map[string]bool{}
	for _, c := range strings.Split(*comms, ",") {
		names[c] = true
	}
	fmt.Printf("%.3f\n", commCPU(names, os.Getpid()))
}
