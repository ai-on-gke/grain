// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitproxy

// Whether a push's update of an existing branch is a fast-forward, read
// from the packfile the push carries (grain/task-2536).
//
// The command list says where a ref was and where it is going, but not
// whether one descends from the other, and that is the whole of the
// difference between an update and a force-push. A declared `push:`
// entry allows a force-move only where it says `force: true`, so for
// every other entry the proxy has to show the update is a fast-forward
// before it forwards it -- and the only evidence it has in hand is the
// commits the push itself is sending.
//
// So this reads them: every commit object in the pack (whole, or a delta
// against another object in the same pack), parsed for its parents, and
// a walk from the new tip back through them looking for the old one. A
// branch moved forward by commits made in the sandbox -- a back-merge, a
// commit on top -- reaches the old tip through commits the pack carries,
// because the client sends exactly the commits the remote lacks.
//
// It fails closed. A pack it cannot read, a commit it cannot resolve (a
// thin pack's delta against an object already on the remote), or a walk
// that runs out of commits in the pack before it reaches the old tip is
// "not shown to be a fast-forward", and the update is refused unless the
// declaration allows force -- except that where the walk runs out
// inside the remote's own history, as it does at once for a branch moved
// to a commit the remote already has (an empty pack), it carries on in
// commits fetched from the remote (ancestry.go).

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
)

// maxPackObjects bounds how many objects a pack may claim before this
// gives up reading it -- and with it, the proof (fail closed).
const maxPackObjects = 1 << 20

// maxPackObjectBytes bounds how many decompressed or delta-reconstructed
// bytes an object in a packfile may expand to before this gives up reading
// it. Git commits and their deltas rarely exceed several kilobytes, so
// 32 MiB is a safe upper bound against decompression bombs and wild delta
// allocations.
const maxPackObjectBytes = 32 << 20

// maxPackCommitBytes bounds the commit data readPackCommits keeps in hand
// across the whole pack -- every commit, and so every delta base, stays in
// memory until the pack is read. maxPackObjectBytes alone bounds one
// object, not the sum: a pack of small deltas each copying its 32 MiB base
// would otherwise keep up to maxPackObjects of them. A real push's commits
// are a few hundred bytes each, so this is far beyond any of them. A
// variable only so a test can lower it.
var maxPackCommitBytes int64 = 256 << 20

// packInflateFloor and packInflateRatio bound the bytes readPackStream
// inflates across the whole pack: packInflateFloor plus packInflateRatio
// times the pack bytes read so far. maxPackObjectBytes bounds one kept
// object only, and a zlib stream of zeros inflates about 1000:1, so a
// pack of many such objects would otherwise cost about 1000 times its
// size in CPU, synchronously in the push request. Real objects inflate a
// few times their compressed size at most -- a file already compressed
// or random, about once -- so an ordinary push, large files and all,
// stays far below this. A variable only so a test can lower it.
var packInflateFloor int64 = 256 << 20

const packInflateRatio = 8

var errPackInflates = errors.New("pack inflates beyond its maximum total size")

// inflateBudget is one object's zlib stream, read against what the whole
// pack may still inflate. The budget grows with the pack bytes read, so
// it is checked on every read rather than fixed when the object starts;
// each read asks for at most one byte past what is left, which is enough
// to know the pack is over it without inflating the rest.
type inflateBudget struct {
	zr       io.Reader
	pack     *packReader
	inflated *int64
}

func (b inflateBudget) Read(p []byte) (int, error) {
	left := packInflateFloor + packInflateRatio*b.pack.n - *b.inflated
	if left < 0 {
		return 0, errPackInflates
	}
	if int64(len(p)) > left+1 {
		p = p[:left+1]
	}
	n, err := b.zr.Read(p)
	if *b.inflated += int64(n); *b.inflated > packInflateFloor+packInflateRatio*b.pack.n {
		return n, errPackInflates
	}
	return n, err
}

// skipToPack advances r past a receive-pack request's command list (and
// push options, if the client sent any) to the packfile behind them, or
// returns an error when there is none to read.
func skipToPack(r *bufio.Reader) error {
	if err := skipPktSection(r); err != nil {
		return err
	}
	if head, err := r.Peek(4); err == nil && string(head) != "PACK" {
		// Push options: a second pkt-line section, ended by its own flush.
		if err := skipPktSection(r); err != nil {
			return err
		}
	}
	if head, err := r.Peek(4); err != nil || string(head) != "PACK" {
		return errNoPack
	}
	return nil
}

// errNoPack is skipToPack's answer to a push that carries no packfile.
var errNoPack = errors.New("no packfile after the command list")

// skipPktSection reads r up to and including the flush-pkt ending the
// pkt-line section at its start.
func skipPktSection(r *bufio.Reader) error {
	var head [4]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return errors.New("pkt-line section ended early")
		}
		n, err := strconv.ParseUint(string(head[:]), 16, 16)
		if err != nil {
			return fmt.Errorf("malformed pkt-line length %q", head[:])
		}
		if n == 0 {
			return nil
		}
		if n < 4 {
			return fmt.Errorf("malformed pkt-line length %d", n)
		}
		if _, err := r.Discard(int(n) - 4); err != nil {
			return fmt.Errorf("malformed pkt-line length %d", n)
		}
	}
}

// packCommits is the parents of every commit a pack carries, by object
// name in hex.
type packCommits map[string][]string

const (
	objCommit   = 1
	objOfsDelta = 6
	objRefDelta = 7
)

// readPackCommits reads every commit in pack. hashLen is 20 for a SHA-1
// repository and 32 for SHA-256, which the push's own object names say.
func readPackCommits(pack []byte, hashLen int) (packCommits, error) {
	return readPackStream(bytes.NewReader(pack), hashLen)
}

// packReader is the pack readPackStream walks, counting what it has read
// so an ofs-delta's base can be found by its offset. It is a flate.Reader
// -- an io.ByteReader -- so zlib reads exactly one object's stream off it
// and not a byte past it, where the next object's header starts.
type packReader struct {
	r flate.Reader
	n int64
}

func (p *packReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	return n, err
}

func (p *packReader) ReadByte() (byte, error) {
	c, err := p.r.ReadByte()
	if err == nil {
		p.n++
	}
	return c, err
}

// readPackStream is readPackCommits over a stream: the pack is read once,
// front to back, and nothing of it is held but the commits -- so a push
// spooled to disk is walked from the file (GitProxy.HandleRequest) rather
// than read back into memory whole.
func readPackStream(pack io.Reader, hashLen int) (packCommits, error) {
	fr, ok := pack.(flate.Reader)
	if !ok {
		fr = bufio.NewReaderSize(pack, 64<<10)
	}
	r := &packReader{r: fr}
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil || string(header[:4]) != "PACK" {
		return nil, errors.New("not a packfile")
	}
	if v := binary.BigEndian.Uint32(header[4:8]); v != 2 && v != 3 {
		return nil, fmt.Errorf("unsupported pack version %d", v)
	}
	count := binary.BigEndian.Uint32(header[8:12])
	if count > maxPackObjects {
		return nil, fmt.Errorf("pack claims %d objects", count)
	}
	newHash := sha1.New
	if hashLen == 32 {
		newHash = sha256.New
	}

	type object struct {
		typ  int
		data []byte // kept for commits only; nil for every other type
	}
	byOffset := map[int64]object{}
	byName := map[string]object{}
	commits := packCommits{}
	var kept, inflated int64

	for i := uint32(0); i < count; i++ {
		start := r.n
		c, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		typ := int(c>>4) & 7
		for shift := 4; c&0x80 != 0; shift += 7 {
			if c, err = r.ReadByte(); err != nil {
				return nil, err
			}
			if shift > 56 {
				return nil, errors.New("object size overflows")
			}
		}
		var base *object
		switch typ {
		case objOfsDelta:
			c, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			ofs := int64(c & 0x7f)
			for c&0x80 != 0 {
				if c, err = r.ReadByte(); err != nil {
					return nil, err
				}
				ofs = ((ofs + 1) << 7) | int64(c&0x7f)
				if ofs > start {
					return nil, errors.New("delta offset out of range")
				}
			}
			if b, ok := byOffset[start-ofs]; ok {
				base = &b
			}
		case objRefDelta:
			name := make([]byte, hashLen)
			if _, err := io.ReadFull(r, name); err != nil {
				return nil, err
			}
			if b, ok := byName[hex.EncodeToString(name)]; ok {
				base = &b
			}
		case 1, 2, 3, 4:
		default:
			return nil, fmt.Errorf("unknown object type %d", typ)
		}
		zr, err := zlib.NewReader(r)
		if err != nil {
			return nil, err
		}
		// Only a commit, or a delta this could resolve against one, is read
		// into memory, and only those are held to maxPackObjectBytes. Every
		// other object -- a blob above all -- is inflated straight into
		// io.Discard: the walk has no use for it, and a file larger than the
		// bound is an ordinary thing for a fast-forward to carry. Either
		// way the read is held to the pack's inflation budget.
		keep := typ == objCommit || base != nil && base.typ == objCommit && base.data != nil
		budgeted := inflateBudget{zr: zr, pack: r, inflated: &inflated}
		var data []byte
		if keep {
			data, err = io.ReadAll(io.LimitReader(budgeted, maxPackObjectBytes+1))
		} else {
			_, err = io.Copy(io.Discard, budgeted)
		}
		_ = zr.Close()
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxPackObjectBytes {
			return nil, errors.New("decompressed pack object exceeds maximum size")
		}

		obj := object{typ: typ}
		if typ == objOfsDelta || typ == objRefDelta {
			// A delta is only worth resolving against a commit: nothing else
			// can produce one, and a base this did not keep (another type,
			// or one outside the pack) leaves it unresolved -- which, if it
			// was a commit the walk needed, is a proof not made.
			obj.typ = 0
			if base != nil && base.typ == objCommit && base.data != nil {
				if resolved, err := applyDelta(base.data, data); err == nil {
					obj = object{typ: objCommit, data: resolved}
				}
			}
		} else if typ == objCommit {
			obj.data = data
		}
		if kept += int64(len(obj.data)); kept > maxPackCommitBytes {
			return nil, errors.New("pack commits exceed maximum total size")
		}
		byOffset[start] = obj
		if obj.typ == objCommit {
			name := objectName(newHash, "commit", obj.data)
			byName[name] = obj
			commits[name] = commitParents(obj.data)
		}
	}
	return commits, nil
}

func objectName(newHash func() hash.Hash, typ string, data []byte) string {
	h := newHash()
	fmt.Fprintf(h, "%s %d\x00", typ, len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// commitParents is the parent lines of one commit object's header.
// Only the header is read: a commit's message can be up to
// maxPackObjectBytes, and copying and splitting all of it per commit was
// garbage on the order of the pack's whole commit data.
func commitParents(data []byte) []string {
	var parents []string
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte("\n"))
		if len(line) == 0 {
			break
		}
		if p, ok := bytes.CutPrefix(line, []byte("parent ")); ok {
			parents = append(parents, string(bytes.TrimSpace(p)))
		}
		data = rest
	}
	return parents
}

// applyDelta rebuilds an object from its base and a git delta.
func applyDelta(base, delta []byte) ([]byte, error) {
	readSize := func() (int, error) {
		size, shift := 0, 0
		for {
			if len(delta) == 0 {
				return 0, errors.New("delta ended early")
			}
			c := delta[0]
			delta = delta[1:]
			size |= int(c&0x7f) << shift
			if c&0x80 == 0 {
				return size, nil
			}
			if shift += 7; shift > 56 {
				return 0, errors.New("delta size overflows")
			}
		}
	}
	srcSize, err := readSize()
	if err != nil {
		return nil, err
	}
	if srcSize != len(base) {
		return nil, errors.New("delta base size mismatch")
	}
	dstSize, err := readSize()
	if err != nil {
		return nil, err
	}
	if dstSize < 0 || int64(dstSize) > maxPackObjectBytes {
		return nil, errors.New("delta target size exceeds limit")
	}
	out := make([]byte, 0, dstSize)
	for len(delta) > 0 {
		op := delta[0]
		delta = delta[1:]
		switch {
		case op&0x80 != 0:
			var off, size int
			for i := 0; i < 4; i++ {
				if op&(1<<i) != 0 {
					if len(delta) == 0 {
						return nil, errors.New("delta ended early")
					}
					off |= int(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<i) != 0 {
					if len(delta) == 0 {
						return nil, errors.New("delta ended early")
					}
					size |= int(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if off+size > len(base) {
				return nil, errors.New("delta copies past its base")
			}
			out = append(out, base[off:off+size]...)
		case op != 0:
			if int(op) > len(delta) {
				return nil, errors.New("delta ended early")
			}
			out = append(out, delta[:op]...)
			delta = delta[op:]
		default:
			return nil, errors.New("reserved delta opcode")
		}
	}
	if len(out) != dstSize {
		return nil, errors.New("delta result size mismatch")
	}
	return out, nil
}

// descends reports whether the commits in c show that newTip descends
// from oldTip: a walk from newTip through parents c carries reaches it.
func (c packCommits) descends(newTip, oldTip string) bool {
	if newTip == oldTip {
		return true
	}
	seen := map[string]bool{}
	queue := []string{newTip}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		for _, p := range c[id] {
			if p == oldTip {
				return true
			}
			queue = append(queue, p)
		}
	}
	return false
}
