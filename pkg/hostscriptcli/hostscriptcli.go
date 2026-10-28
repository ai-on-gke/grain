// Package hostscriptcli is `grain host-script`, the sandbox side of the
// host-script capability (docs/host-scripts.md): it runs a granted host
// script by name, sending its argv to the grant's endpoint on grain's
// side and replaying the framed answer as a local command's stdout,
// stderr and exit status.
//
// A grant places one descriptor per script, Dir/<name>.json, naming the
// endpoint and carrying its token. This package imports only the
// standard library, so the guest image can build the sandbox's own
// `grain` (cmd/grain-sandbox) from it without the rest of grain.
package hostscriptcli

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Dir is where a grant places its descriptor in the sandbox.
const Dir = "/home/debian/.host-script"

// DirEnv names another descriptor directory than Dir; tests use it.
const DirEnv = "GRAIN_HOST_SCRIPT_DIR"

// ArgvType is the exec request's content type: one argument per line,
// each hex-encoded, so any byte -- a newline, a quote -- is carried
// without escaping and arrives exactly as it was given.
const ArgvType = "text/vnd.grain.argv-hex"

// ExitProtocol is the status of a call grain could not make or answer:
// no such grant, a refused token, an endpoint that is gone.
const ExitProtocol = 125

// Descriptor is one grant's Dir/<name>.json.
type Descriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage,omitempty"`
	Port        int    `json:"port"`
	AttemptID   string `json:"attempt_id"`
	Token       string `json:"token"`
}

// URL is the descriptor's exec endpoint.
func (d Descriptor) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/runs/%s/exec", d.Port, d.AttemptID)
}

// Help is what `grain host-script <name> --grain-help` prints.
func (d Descriptor) Help() string {
	help := "Usage: grain host-script " + d.Name + " [arguments...]\n\n" + d.Description + "\n"
	if d.Usage != "" {
		help += "\n" + strings.TrimRight(d.Usage, "\n") + "\n"
	}
	return help + "\nRuns on grain's side, not in this sandbox; arguments are passed through exactly, without a shell, and are logged.\n"
}

var scriptName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

const usage = `usage: grain host-script <name> [arguments...]
       grain host-script <name> --grain-help   print the script's description and usage
       grain host-script --help <name>         the same
       grain host-script --list                list the host scripts granted to this sandbox
`

// Main runs `grain host-script` with args (those after "host-script")
// and returns its exit status.
func Main(args []string, stdout, stderr io.Writer) int {
	dir := os.Getenv(DirEnv)
	if dir == "" {
		dir = Dir
	}
	return Run(dir, args, stdout, stderr)
}

// Run is Main reading descriptors from dir.
func Run(dir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "--list", "-list", "--grain-list":
		return list(dir, stdout, stderr)
	case "--help", "-help", "-h", "--grain-help":
		if len(args) < 2 {
			fmt.Fprint(stdout, usage)
			return 0
		}
		d, code := load(dir, args[1], stderr)
		if d == nil {
			return code
		}
		fmt.Fprint(stdout, d.Help())
		return 0
	}
	d, code := load(dir, args[0], stderr)
	if d == nil {
		return code
	}
	if len(args) >= 2 && args[1] == "--grain-help" {
		fmt.Fprint(stdout, d.Help())
		return 0
	}
	return Call(*d, args[1:], stdout, stderr)
}

func load(dir, name string, stderr io.Writer) (*Descriptor, int) {
	if !scriptName.MatchString(name) {
		fmt.Fprintf(stderr, "grain host-script: %q is not a host script name\n", name)
		return nil, 2
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "grain host-script: no host script %q is granted to this sandbox (or its grant was released); `grain host-script --list` shows the ones that are\n", name)
		return nil, ExitProtocol
	}
	if err != nil {
		fmt.Fprintf(stderr, "grain host-script: %s: %v\n", name, err)
		return nil, ExitProtocol
	}
	var d Descriptor
	if err := json.Unmarshal(b, &d); err != nil || d.Port <= 0 || d.AttemptID == "" {
		fmt.Fprintf(stderr, "grain host-script: %s: unreadable grant descriptor\n", name)
		return nil, ExitProtocol
	}
	if d.Name == "" {
		d.Name = name
	}
	return &d, 0
}

func list(dir string, stdout, stderr io.Writer) int {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(paths)
	n := 0
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		d, _ := load(dir, name, io.Discard)
		if d == nil {
			continue
		}
		fmt.Fprintf(stdout, "%s\t%s\n", d.Name, d.Description)
		n++
	}
	if n == 0 {
		fmt.Fprintln(stderr, "grain host-script: no host scripts are granted to this sandbox")
	}
	return 0
}

// Call sends argv to d's endpoint and replays the answer: "o <bytes>"
// frames to stdout and "e <bytes>" to stderr as they arrive, then the
// status of "x <status>". A call with no exit frame is ExitProtocol.
func Call(d Descriptor, argv []string, stdout, stderr io.Writer) int {
	var body strings.Builder
	for _, a := range argv {
		body.WriteString(hex.EncodeToString([]byte(a)) + "\n")
	}
	req, err := http.NewRequest(http.MethodPost, d.URL(), strings.NewReader(body.String()))
	if err != nil {
		fmt.Fprintf(stderr, "grain host-script: %s: %v\n", d.Name, err)
		return ExitProtocol
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("Content-Type", ArgvType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "grain host-script: %s: no answer from grain's host-script endpoint (the grant was released, or it is unreachable): %v\n", d.Name, err)
		return ExitProtocol
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if strings.HasSuffix(line, "\n") {
			line = line[:len(line)-1]
			switch {
			case strings.HasPrefix(line, "o "):
				stdout.Write(DecodeFrame(line[2:]))
			case strings.HasPrefix(line, "e "):
				stderr.Write(DecodeFrame(line[2:]))
			case strings.HasPrefix(line, "x "):
				var code int
				if _, err := fmt.Sscan(line[2:], &code); err != nil || code < 0 || code > 255 {
					code = ExitProtocol
				}
				return code
			}
		}
		if err != nil {
			break
		}
	}
	fmt.Fprintf(stderr, "grain host-script: %s: no answer from grain's host-script endpoint (the grant was released, or it is unreachable)\n", d.Name)
	return ExitProtocol
}

// DecodeFrame undoes the server's frame encoding: bytes pass as
// themselves, except a backslash and three octal digits, which is that
// byte (a printf format of literals and octal escapes).
func DecodeFrame(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			out = append(out, (s[i+1]-'0')<<6|(s[i+2]-'0')<<3|(s[i+3]-'0'))
			i += 3
			continue
		}
		out = append(out, c)
	}
	return out
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }
