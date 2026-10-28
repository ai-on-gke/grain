package agent

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
)

// passwdPath and groupPath are variables so the tests can point them at
// fixtures; nothing else reassigns them.
var (
	passwdPath = "/etc/passwd"
	groupPath  = "/etc/group"
)

// lookupAccount resolves name against the guest's own /etc/passwd and
// /etc/group.
//
// These are read directly rather than through os/user because os/user
// answers neither of the two things that actually matter here: the login
// shell (its User struct has no field for it) and the supplementary
// groups (its GroupIds needs cgo to be reliable, and this binary is
// built CGO_ENABLED=0 so that one artifact runs on both the Debian and
// the Alpine guest). Both files are the plain colon-separated format on
// either guest.
//
// An empty name means root, matching guestexec's own default.
func lookupAccount(name string) (*account, error) {
	if name == "" {
		name = "root"
	}

	a, err := readPasswd(name)
	if err != nil {
		return nil, err
	}
	if a.Shell == "" {
		a.Shell = "/bin/sh"
	}
	if a.Home == "" {
		a.Home = "/"
	}
	// A home directory named in /etc/passwd but not present would make
	// every command fail on chdir, which reads as the command being
	// broken rather than the account being half-created. Root of the
	// filesystem is a worse working directory and a much better error.
	if fi, err := os.Stat(a.Home); err != nil || !fi.IsDir() {
		a.Home = "/"
	}

	a.self = a.UID == os.Getuid()
	if !a.self {
		a.Groups = readGroups(a.Name, a.GID)
	}
	return a, nil
}

// commandShell is what a *command* for this account is run under, as
// against the login shell an interactive session gets.
//
// They differ for exactly the accounts a service runs as. A guest gives
// its redis or its www-data account /usr/sbin/nologin or /bin/false,
// whose entire job is to refuse a login -- so running a command through
// it answers "This account is currently not available" and exits 1,
// whatever the command was. But a command run as an account is not a
// login: docker's own USER is a uid and a gid, and so is the `USER` a
// Granulefile writes above a `RUN` in a VM stage (§6). The credential
// below is what makes it that account; the shell here only has to be
// able to run the line.
//
// An interactive session keeps the login shell, and so keeps refusing:
// "granule exec -u redis" with no command is a login, and nologin is
// the guest's answer to it.
func (a *account) commandShell() string {
	switch path.Base(a.Shell) {
	case "nologin", "false":
		return "/bin/sh"
	}
	return a.Shell
}

func readPasswd(name string) (*account, error) {
	f, err := os.Open(passwdPath)
	if err != nil {
		return nil, fmt.Errorf("reading the guest's accounts: %w", err)
	}
	defer f.Close()

	// A numeric name is a uid, and has to be one setuid(2) can actually
	// switch to. Atoi would take "4294967296" and the credential would
	// truncate it to 0, and 4294967295 is (uid_t)-1, which setuid reads as
	// "leave the uid alone": either way a session asked for an
	// unprivileged uid would run as root, the account the agent already
	// is, while claiming otherwise.
	var numericUID *int
	if uid, err := strconv.ParseUint(name, 10, 32); err == nil {
		if uid == 1<<32-1 {
			return nil, fmt.Errorf("uid %s is (uid_t)-1, which no account can have", name)
		}
		n := int(uid)
		numericUID = &n
	}

	sc := bufio.NewScanner(f)
	var byUID *account
	for sc.Scan() {
		// name:passwd:uid:gid:gecos:home:shell
		fields := strings.Split(sc.Text(), ":")
		if len(fields) < 7 {
			continue
		}
		acct := &account{
			Name:  fields[0],
			UID:   atoi(fields[2]),
			GID:   atoi(fields[3]),
			Home:  fields[5],
			Shell: fields[6],
		}
		if fields[0] == name {
			return acct, nil
		}
		if numericUID != nil && acct.UID == *numericUID && byUID == nil {
			byUID = acct
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the guest's accounts: %w", err)
	}
	if byUID != nil {
		return byUID, nil
	}
	if numericUID != nil {
		// A uid with no entry is still run as that uid, the way
		// docker's own `USER 12345` and a Granulefile's numeric USER
		// are. Its group is the matching gid rather than root's group
		// (which is what docker gives it): a numeric user is how a
		// caller asks for an unprivileged account, and gid 0 would hand
		// it every root-group-writable file in the guest.
		return &account{
			Name:  name,
			UID:   *numericUID,
			GID:   *numericUID,
			Home:  "/",
			Shell: "/bin/sh",
		}, nil
	}
	return nil, fmt.Errorf("no account %q on this guest", name)
}

// readGroups returns the supplementary groups name belongs to, always
// including its own primary gid.
//
// Failure here is deliberately not fatal: a guest with no readable
// /etc/group is one where the primary group alone is the best available
// answer, and refusing to run anything at all would be a worse one.
func readGroups(name string, gid int) []uint32 {
	groups := []uint32{uint32(gid)}
	seen := map[uint32]bool{uint32(gid): true}

	f, err := os.Open(groupPath)
	if err != nil {
		return groups
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// name:passwd:gid:member,member,...
		fields := strings.Split(sc.Text(), ":")
		if len(fields) < 4 {
			continue
		}
		for _, member := range strings.Split(fields[3], ",") {
			if member != name {
				continue
			}
			g := uint32(atoi(fields[2]))
			if !seen[g] {
				seen[g] = true
				groups = append(groups, g)
			}
		}
	}
	return groups
}
