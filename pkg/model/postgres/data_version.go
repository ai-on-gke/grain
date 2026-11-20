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

package postgres

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Per-connection PRAGMA data_version emulation for PostgreSQL.
//
// SQLite's PRAGMA data_version moves whenever ANY *other* connection commits
// a write, and holds still for writes committed on the connection asking the
// pragma. Store.ChangeToken relies on the first half for cross-process and
// same-pool writes, and live-run batch flushing relies on the second half by
// executing coalesced activity/transcript flushes on ChangeToken's own
// dedicated connection so they do not invalidate UI caches.
//
// On PostgreSQL, txid_current_snapshot() advances for every transaction that
// gets an xid, including one committed on this connection. By recording the
// xid assigned to any read-write transaction committed on a connection that
// is serving PRAGMA data_version, we distinguish commits made by this
// connection from commits made by any other connection or process.

const maxDataVersionSnapshotSpan = 1000

type pgSnapshot struct {
	xmin int64
	xmax int64
	xip  map[int64]struct{}
}

func parsePGSnapshot(raw string) (pgSnapshot, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 3 {
		return pgSnapshot{}, false
	}
	xmin, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return pgSnapshot{}, false
	}
	xmax, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return pgSnapshot{}, false
	}
	var xip map[int64]struct{}
	if parts[2] != "" {
		items := strings.Split(parts[2], ",")
		xip = make(map[int64]struct{}, len(items))
		for _, item := range items {
			id, err := strconv.ParseInt(item, 10, 64)
			if err != nil {
				return pgSnapshot{}, false
			}
			xip[id] = struct{}{}
		}
	}
	return pgSnapshot{xmin: xmin, xmax: xmax, xip: xip}, true
}

func (s pgSnapshot) isDone(x int64) bool {
	if x < s.xmin {
		return true
	}
	if x >= s.xmax {
		return false
	}
	_, inProgress := s.xip[x]
	return !inProgress
}

type dataVersionState struct {
	initialized bool
	version     int64
	prev        pgSnapshot
	ownXids     map[int64]struct{}
}

func (d *dataVersionState) reset() {
	d.initialized = false
	d.version = 0
	d.prev = pgSnapshot{}
	d.ownXids = nil
}

func (d *dataVersionState) recordOwnXid(xid int64) {
	if !d.initialized {
		return
	}
	if d.ownXids == nil {
		d.ownXids = make(map[int64]struct{})
	}
	d.ownXids[xid] = struct{}{}
}

func (d *dataVersionState) observe(raw string) int64 {
	curr, ok := parsePGSnapshot(raw)
	if !ok {
		d.version++
		d.initialized = false
		d.ownXids = nil
		return d.version
	}
	if !d.initialized {
		d.initialized = true
		d.version = 1
		d.prev = curr
		d.ownXids = nil
		return d.version
	}

	externalChange := false
	if curr.xmin < d.prev.xmin || curr.xmax < d.prev.xmax || curr.xmax-d.prev.xmin > maxDataVersionSnapshotSpan {
		externalChange = true
	} else {
		for x := d.prev.xmin; x < curr.xmax; x++ {
			if !d.prev.isDone(x) && curr.isDone(x) {
				if _, own := d.ownXids[x]; !own {
					externalChange = true
					break
				}
			}
		}
	}

	if externalChange {
		d.version++
		clear(d.ownXids)
	} else {
		for x := range d.ownXids {
			if x < curr.xmin {
				delete(d.ownXids, x)
			}
		}
	}
	d.prev = curr
	return d.version
}

func isPragmaDataVersion(query string) bool {
	upper := strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(upper, "PRAGMA ") && strings.Contains(upper, "DATA_VERSION")
}

func (c *adaptedConn) queryDataVersion(ctx context.Context) (driver.Rows, error) {
	raw, err := querySingleString(ctx, c.inner, "SELECT txid_current_snapshot()::text")
	if err != nil {
		return nil, err
	}
	return &singleInt64Rows{col: "data_version", val: c.dv.observe(raw)}, nil
}

func querySingleString(ctx context.Context, conn driver.Conn, query string) (string, error) {
	var (
		rows driver.Rows
		err  error
	)
	if qc, ok := conn.(driver.QueryerContext); ok {
		rows, err = qc.QueryContext(ctx, query, nil)
	} else {
		stmt, perr := conn.Prepare(query)
		if perr != nil {
			return "", perr
		}
		defer stmt.Close()
		rows, err = stmt.Query(nil)
	}
	if err != nil {
		return "", err
	}
	defer rows.Close()
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		return "", err
	}
	switch v := dest[0].(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return fmt.Sprint(v), nil
	}
}

func queryCurrentXidIfAssigned(ctx context.Context, conn driver.Conn) (int64, bool) {
	var (
		rows driver.Rows
		err  error
	)
	const q = "SELECT txid_current_if_assigned()"
	if qc, ok := conn.(driver.QueryerContext); ok {
		rows, err = qc.QueryContext(ctx, q, nil)
	} else {
		stmt, perr := conn.Prepare(q)
		if perr != nil {
			return 0, false
		}
		defer stmt.Close()
		rows, err = stmt.Query(nil)
	}
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil || dest[0] == nil {
		return 0, false
	}
	switch v := dest[0].(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case uint64:
		return int64(v), true
	case uint32:
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	case []byte:
		n, err := strconv.ParseInt(string(v), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

type dataVersionTx struct {
	inner driver.Tx
	conn  *adaptedConn
}

func (t *dataVersionTx) Commit() error {
	xid, hasXid := queryCurrentXidIfAssigned(context.Background(), t.conn.inner)
	if err := t.inner.Commit(); err != nil {
		return err
	}
	if hasXid {
		t.conn.dv.recordOwnXid(xid)
	}
	return nil
}

func (t *dataVersionTx) Rollback() error {
	return t.inner.Rollback()
}

type singleInt64Rows struct {
	col  string
	val  int64
	done bool
}

func (r *singleInt64Rows) Columns() []string { return []string{r.col} }
func (r *singleInt64Rows) Close() error      { return nil }
func (r *singleInt64Rows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.val
	return nil
}
