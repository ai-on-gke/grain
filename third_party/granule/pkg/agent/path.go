package agent

import (
	"bufio"
	"os"
	"strings"
)

// loginDefsPath is a variable so the tests can point it at a fixture;
// nothing else reassigns it.
var loginDefsPath = "/etc/login.defs"

// The PATHs to fall back on when /etc/login.defs is missing or says
// nothing about them. These are shadow's own compiled-in defaults, which
// are also what Debian and Alpine ship in the file.
const (
	fallbackSuPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	fallbackPATH   = "/usr/local/bin:/usr/bin:/bin:/usr/games"
)

// defaultPATH is the PATH a login session gets when no profile has been
// read, which is the case for every command run over this transport.
//
// It is deliberately not one string: root's PATH has the sbin
// directories on it and an ordinary user's does not, and that difference
// is load-bearing. useradd, ip, and mount all live in /usr/sbin, so a
// root session without them fails on "useradd: not found" rather than on
// anything that names the real problem.
//
// Both values come from /etc/login.defs when the guest has one, because
// that is the file login(1) and sshd(8) read to answer this same
// question, and a guest that has edited it means it.
func defaultPATH(uid int) string {
	key, fallback := "ENV_PATH", fallbackPATH
	if uid == 0 {
		key, fallback = "ENV_SUPATH", fallbackSuPATH
	}
	if v := loginDefs(key); v != "" {
		return v
	}
	return fallback
}

// loginDefs returns the value of key in /etc/login.defs, or "" if the
// file cannot be read or does not set it.
//
// The format is one "KEY value" per line, # to end of line is a comment,
// and the two PATH entries conventionally write their value as
// "PATH=..." -- shadow strips that prefix, so this does too.
func loginDefs(key string) string {
	f, err := os.Open(loginDefsPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != key {
			continue
		}
		return strings.TrimPrefix(fields[1], "PATH=")
	}
	return ""
}
