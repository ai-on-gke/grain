package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Operator notifications (docs/design/durable-tasks.md, DT-5): something
// a person should know about or act on, raised by a reconciler, an agent
// (notify_operator) or the API, which never parks whoever raised it.
//
// One open notification per subject: raising an open subject bumps it
// rather than adding a second row (RaiseNotification), and it stays open
// until its raiser, a person, or its clears_when condition clears it.
// Acknowledging is separate from clearing -- it hides the notification
// from the Alerts count and nothing else.

// NotificationSeverity is how loudly a notification asks for attention,
// in alerts.js's own words.
type NotificationSeverity string

const (
	NotificationCritical NotificationSeverity = "critical"
	NotificationWarning  NotificationSeverity = "warning"
	NotificationInfo     NotificationSeverity = "info"
)

// Rank orders severities: critical highest. An unknown word ranks with
// info.
func (s NotificationSeverity) Rank() int {
	switch s {
	case NotificationCritical:
		return 2
	case NotificationWarning:
		return 1
	}
	return 0
}

// ParseNotificationSeverity reads a severity, "" as warning.
func ParseNotificationSeverity(raw string) (NotificationSeverity, error) {
	switch s := NotificationSeverity(strings.ToLower(strings.TrimSpace(raw))); s {
	case "":
		return NotificationWarning, nil
	case NotificationCritical, NotificationWarning, NotificationInfo:
		return s, nil
	}
	return "", fmt.Errorf("severity %q: want critical, warning or info", raw)
}

// The kinds of NotificationClear.
const (
	// ClearsOnTask clears when a task closes (Until "closed") or merges
	// (Until "merged") -- a WaitTarget settling.
	ClearsOnTask = "task"
	// ClearsOnBaseGreen clears when a repository's base branch's latest
	// recorded grain CI verdict is a success.
	ClearsOnBaseGreen = "base-green"
	// ClearsOnTTL clears at a time.
	ClearsOnTTL = "ttl"
)

// Notification subjects raised by steward verification (DT-14).
const (
	// SubjectStewardReportMismatch is raised when a steward wake's report
	// fails stewardReportCheck (uncited claims, cited-but-unread IDs, or
	// attribute mismatches against kind tags and state).
	SubjectStewardReportMismatch = "agent.steward:report-mismatch"
	// SubjectStewardRegression is raised when the 7-day rolling steward
	// report-check failure rate passes 5% of claims.
	SubjectStewardRegression = "pipeline.steward-regression"
)

// NotificationClear is a notification's clears_when: the condition
// reconcileNotifications checks each cycle.
type NotificationClear struct {
	Kind   string     `json:"kind"`
	Task   string     `json:"task,omitempty"`
	Until  string     `json:"until,omitempty"`
	Repo   string     `json:"repo,omitempty"`
	Branch string     `json:"branch,omitempty"`
	At     *time.Time `json:"at,omitempty"`
	// TTL is how a caller asks for a ClearsOnTTL condition ("6h"); the
	// store turns it into At at the first raise and keeps only At.
	TTL string `json:"ttl,omitempty"`
}

// Validate checks c names a condition grain can evaluate, filling At
// from TTL against now.
func (c *NotificationClear) Validate(now time.Time) error {
	switch c.Kind {
	case ClearsOnTask:
		if strings.TrimSpace(c.Task) == "" {
			return errors.New("clears_when kind task needs a task")
		}
		c.Task = NormalizeTaskID(c.Task)
		switch c.Until {
		case "":
			c.Until = "closed"
		case "closed", "merged":
		default:
			return fmt.Errorf("clears_when until %q: want closed or merged", c.Until)
		}
	case ClearsOnBaseGreen:
		if _, err := ParseRepo(c.Repo); err != nil {
			return fmt.Errorf("clears_when kind base-green: %w", err)
		}
		if strings.TrimSpace(c.Branch) == "" {
			return errors.New("clears_when kind base-green needs a branch")
		}
	case ClearsOnTTL:
		if c.At == nil {
			d, err := time.ParseDuration(strings.TrimSpace(c.TTL))
			if err != nil || d <= 0 {
				return fmt.Errorf("clears_when kind ttl needs a positive ttl (6h, 90m) or an at time")
			}
			at := now.Add(d).UTC()
			c.At = &at
		}
		c.TTL = ""
	default:
		return fmt.Errorf("clears_when kind %q: want task, base-green or ttl", c.Kind)
	}
	return nil
}

// NotificationDelivery is where a notification instance has been
// delivered, so each place gets it once.
type NotificationDelivery struct {
	// Director is the comment id the director chat was given it as.
	Director int64    `json:"director,omitempty"`
	External []string `json:"external,omitempty"`
}

// Notification is one row of the notification table.
type Notification struct {
	ID       int64                `json:"id"`
	Subject  string               `json:"subject"`
	Family   string               `json:"family"`
	Severity NotificationSeverity `json:"severity"`
	Title    string               `json:"title"`
	Body     string               `json:"body"`
	TaskID   string               `json:"taskId,omitempty"`
	Repo     string               `json:"repo,omitempty"`
	// RaisedBy is the principal that raised it, as "kind:id"
	// (PrincipalString).
	RaisedBy     string             `json:"raisedBy"`
	RaisedAt     time.Time          `json:"raisedAt"`
	LastRaisedAt time.Time          `json:"lastRaisedAt"`
	RaiseCount   int                `json:"raiseCount"`
	ClearsWhen   *NotificationClear `json:"clearsWhen,omitempty"`
	ClearedAt    *time.Time         `json:"clearedAt,omitempty"`
	ClearedBy    string             `json:"clearedBy,omitempty"`
	AckedAt      *time.Time         `json:"ackedAt,omitempty"`
	AckedBy      string             `json:"ackedBy,omitempty"`
	// Actions are the offered actions, by name ("investigate",
	// "open-task", "retry-merge", ...).
	Actions             []string             `json:"actions,omitempty"`
	InvestigationTaskID string               `json:"investigationTaskId,omitempty"`
	Delivered           NotificationDelivery `json:"delivered"`
}

// Open reports whether n has not been cleared.
func (n Notification) Open() bool { return n.ClearedAt == nil }

// NotificationFamily is the part of a subject before its first ':' -- the
// key a subscription names.
func NotificationFamily(subject string) string {
	family, _, _ := strings.Cut(subject, ":")
	return family
}

// PrincipalString is p as notification.raised_by writes it.
func PrincipalString(p Principal) string {
	return string(p.Kind) + ":" + p.ID
}

// The notification families grain's own escalations are raised under
// (docs/design/durable-tasks.md, DT-5's "Existing escalations, mapped").
// A subject is the family, ':', and what it is about.
const (
	// FamilyBaseRed is a confirmed-red base branch, "<repo>@<branch>".
	FamilyBaseRed = "pipeline.base-red"
	// FamilyNotPublishing is a base branch nothing has published from
	// (BaseBranchPublishing), "<repo>@<branch>".
	FamilyNotPublishing = "pipeline.not-publishing"
	// FamilyMergeStalled is a merge-queue entry handed to a person past
	// its patience, "<task>".
	FamilyMergeStalled = "pipeline.merge-stalled"
	// FamilyMergeBatch is a merge batch that failed to answer or ran
	// long, "<repo>@<branch>" (one batch per base branch at a time).
	FamilyMergeBatch = "pipeline.batch"
	// FamilyRepairFailing is a base-branch repair that is not getting
	// the branch green, "<task>".
	FamilyRepairFailing = "pipeline.repair-failing"
	// FamilyForceMergeRequest asks a person to force-merge "<task>".
	FamilyForceMergeRequest = "request.force-merge"
	// FamilyRevertRequest asks a person to revert "<task or sha>".
	FamilyRevertRequest = "request.revert"
	// FamilyReleasePending is a trunk release whose soak passed and that
	// waits for a person to approve or reject it, "<vX.Y.0>"
	// (docs/design/release-from-trunk.md, §4). Raised by the release
	// train through the release gate, with ReleasePendingActions.
	FamilyReleasePending = "release.pending"
)

// ReleasePendingActions are the actions a FamilyReleasePending
// notification offers: the UI's Approve and Reject buttons, which call
// the person-only pending-releases routes.
var ReleasePendingActions = []string{"approve-release", "reject-release"}

// NotificationSubject is family's subject for key.
func NotificationSubject(family, key string) string { return family + ":" + key }

// BranchKey is the "<repo>@<branch>" a branch's subjects end in.
func BranchKey(repo RepoRef, branch string) string { return repo.String() + "@" + branch }

// defaultRequestClear fills in what the request.* families mean, for a
// raiser (the steward, any api:task-admin holder) that gave only the
// subject: a force-merge request is about the task it names and clears
// when that task merges or closes; a revert request clears when its
// revert task -- the notification's own task, where it names one --
// merges, and otherwise waits for a person to dismiss it. The task a
// revert subject names is the change being reverted, already merged, so
// it is never the condition.
func defaultRequestClear(n *Notification) {
	family, key, _ := strings.Cut(n.Subject, ":")
	key = strings.TrimSpace(key)
	switch family {
	case FamilyForceMergeRequest:
		if key == "" {
			return
		}
		if n.TaskID == "" {
			n.TaskID = NormalizeTaskID(key)
		}
		if n.ClearsWhen == nil {
			n.ClearsWhen = &NotificationClear{Kind: ClearsOnTask, Task: NormalizeTaskID(key), Until: "closed"}
		}
	case FamilyRevertRequest:
		if n.TaskID != "" && n.ClearsWhen == nil {
			n.ClearsWhen = &NotificationClear{Kind: ClearsOnTask, Task: n.TaskID, Until: "merged"}
		}
	}
}

// NotificationRaise is the outcome of one RaiseNotification.
type NotificationRaise struct {
	Notification Notification
	// New is a first raise: a new instance.
	New bool
	// Rose is a raise of an open instance that raised its severity.
	Rose bool
}

const notificationColumns = "`id`,`subject`,`family`,`severity`,`title`,`body`,`task_id`,`repo`,`raised_by`," +
	"`raised_at`,`last_raised_at`,`raise_count`,`clears_when`,`cleared_at`,`cleared_by`,`acked_at`,`acked_by`," +
	"`actions`,`investigation_task_id`,`delivered`"

func scanNotification(scan func(...any) error) (Notification, error) {
	var n Notification
	var severity string
	var taskID, repo, clears, clearedBy, ackedBy, actions, investigation, delivered sql.NullString
	var clearedAt, ackedAt sql.NullTime
	if err := scan(&n.ID, &n.Subject, &n.Family, &severity, &n.Title, &n.Body, &taskID, &repo, &n.RaisedBy,
		&n.RaisedAt, &n.LastRaisedAt, &n.RaiseCount, &clears, &clearedAt, &clearedBy, &ackedAt, &ackedBy,
		&actions, &investigation, &delivered); err != nil {
		return Notification{}, err
	}
	n.Severity = NotificationSeverity(severity)
	n.TaskID, n.Repo = taskID.String, repo.String
	n.RaisedAt, n.LastRaisedAt = n.RaisedAt.UTC(), n.LastRaisedAt.UTC()
	if clears.String != "" {
		var c NotificationClear
		if json.Unmarshal([]byte(clears.String), &c) == nil {
			n.ClearsWhen = &c
		}
	}
	if clearedAt.Valid {
		t := clearedAt.Time.UTC()
		n.ClearedAt = &t
	}
	n.ClearedBy = clearedBy.String
	if ackedAt.Valid {
		t := ackedAt.Time.UTC()
		n.AckedAt = &t
	}
	n.AckedBy = ackedBy.String
	if actions.String != "" {
		_ = json.Unmarshal([]byte(actions.String), &n.Actions)
	}
	n.InvestigationTaskID = investigation.String
	if delivered.String != "" {
		_ = json.Unmarshal([]byte(delivered.String), &n.Delivered)
	}
	return n, nil
}

func jsonOrNull(v any, empty bool) (any, error) {
	if empty {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func openNotificationBySubject(ctx context.Context, q querier, subject string) (*Notification, error) {
	row := q.QueryRowContext(ctx, "SELECT "+notificationColumns+
		" FROM `notification` WHERE `subject` = ? AND `cleared_at` IS NULL", subject)
	n, err := scanNotification(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// RaiseNotification raises n, or bumps the open instance of its subject:
// last_raised_at and raise_count move, the title, body and actions are
// replaced by the newer raise's, and the severity may rise but never
// fall. A raise whose severity rose re-surfaces an acknowledged instance
// (its ack is dropped); any other raise of an acknowledged one leaves it
// acknowledged. n.RaisedAt is the raise's moment; zero is now.
func (s *Store) RaiseNotification(ctx context.Context, n Notification) (NotificationRaise, error) {
	n.Subject = strings.TrimSpace(n.Subject)
	if n.Subject == "" {
		return NotificationRaise{}, errors.New("a notification needs a subject")
	}
	if strings.TrimSpace(n.Title) == "" {
		return NotificationRaise{}, errors.New("a notification needs a title")
	}
	if n.Severity == "" {
		n.Severity = NotificationWarning
	}
	if _, err := ParseNotificationSeverity(string(n.Severity)); err != nil {
		return NotificationRaise{}, err
	}
	now := n.RaisedAt
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	defaultRequestClear(&n)
	if n.ClearsWhen != nil {
		if err := n.ClearsWhen.Validate(now); err != nil {
			return NotificationRaise{}, err
		}
	}
	n.Family = NotificationFamily(n.Subject)
	var out NotificationRaise
	err := s.write(ctx, "raise notification "+n.Subject, func(tx *sql.Tx) error {
		open, err := openNotificationBySubject(ctx, tx, n.Subject)
		if err != nil {
			return fmt.Errorf("reading notification %s: %w", n.Subject, err)
		}
		actions, err := jsonOrNull(n.Actions, len(n.Actions) == 0)
		if err != nil {
			return err
		}
		if open == nil {
			clears, err := jsonOrNull(n.ClearsWhen, n.ClearsWhen == nil)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, "INSERT INTO `notification` (`subject`,`family`,`severity`,`title`,`body`,"+
				"`task_id`,`repo`,`raised_by`,`raised_at`,`last_raised_at`,`raise_count`,`clears_when`,`actions`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,1,?,?)",
				n.Subject, n.Family, string(n.Severity), n.Title, n.Body, nullable(n.TaskID), nullable(n.Repo),
				n.RaisedBy, now, now, clears, actions)
			if err != nil {
				return fmt.Errorf("raising notification %s: %w", n.Subject, err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("reading the new notification's id: %w", err)
			}
			n.ID, n.RaisedAt, n.LastRaisedAt, n.RaiseCount = id, now, now, 1
			out = NotificationRaise{Notification: n, New: true}
			return nil
		}
		severity := open.Severity
		rose := n.Severity.Rank() > severity.Rank()
		if rose {
			severity = n.Severity
		}
		ack := "`acked_at`"
		ackBy := "`acked_by`"
		if rose {
			ack, ackBy = "NULL", "NULL"
		}
		if _, err := tx.ExecContext(ctx, "UPDATE `notification` SET `severity` = ?, `title` = ?, `body` = ?, "+
			"`last_raised_at` = ?, `raise_count` = `raise_count` + 1, `actions` = COALESCE(?, `actions`), "+
			"`acked_at` = "+ack+", `acked_by` = "+ackBy+" WHERE `id` = ?",
			string(severity), n.Title, n.Body, now, actions, open.ID); err != nil {
			return fmt.Errorf("raising notification %s again: %w", n.Subject, err)
		}
		bumped, err := getNotification(ctx, tx, open.ID)
		if err != nil {
			return err
		}
		out = NotificationRaise{Notification: *bumped, Rose: rose}
		return nil
	})
	return out, err
}

func getNotification(ctx context.Context, q querier, id int64) (*Notification, error) {
	row := q.QueryRowContext(ctx, "SELECT "+notificationColumns+" FROM `notification` WHERE `id` = ?", id)
	n, err := scanNotification(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading notification %d: %w", id, err)
	}
	return &n, nil
}

// GetNotification is notification id, or nil.
func (s *Store) GetNotification(ctx context.Context, id int64) (*Notification, error) {
	return getNotification(ctx, s.db, id)
}

// OpenNotification is the open instance of subject, or nil.
func (s *Store) OpenNotification(ctx context.Context, subject string) (*Notification, error) {
	return openNotificationBySubject(ctx, s.db, subject)
}

// ClearNotification clears the open instance of subject, if there is
// one, recording by as who cleared it. It reports whether it cleared
// anything.
func (s *Store) ClearNotification(ctx context.Context, subject, by string) (bool, error) {
	var cleared bool
	err := s.write(ctx, "clear notification "+subject, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE `notification` SET `cleared_at` = ?, `cleared_by` = ? "+
			"WHERE `subject` = ? AND `cleared_at` IS NULL", time.Now().UTC(), by, subject)
		if err != nil {
			return fmt.Errorf("clearing notification %s: %w", subject, err)
		}
		n, _ := res.RowsAffected()
		cleared = n > 0
		return nil
	})
	return cleared, err
}

// ClearNotificationByID clears notification id if it is open.
func (s *Store) ClearNotificationByID(ctx context.Context, id int64, by string) (bool, error) {
	var cleared bool
	err := s.write(ctx, fmt.Sprintf("clear notification %d", id), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE `notification` SET `cleared_at` = ?, `cleared_by` = ? "+
			"WHERE `id` = ? AND `cleared_at` IS NULL", time.Now().UTC(), by, id)
		if err != nil {
			return fmt.Errorf("clearing notification %d: %w", id, err)
		}
		n, _ := res.RowsAffected()
		cleared = n > 0
		return nil
	})
	return cleared, err
}

// AckNotification acknowledges notification id. Acknowledging an
// acknowledged one keeps the first acknowledgement.
func (s *Store) AckNotification(ctx context.Context, id int64, by string, at time.Time) error {
	return s.write(ctx, fmt.Sprintf("acknowledge notification %d", id), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `notification` SET `acked_at` = ?, `acked_by` = ? "+
			"WHERE `id` = ? AND `acked_at` IS NULL", at.UTC(), by, id)
		if err != nil {
			return fmt.Errorf("acknowledging notification %d: %w", id, err)
		}
		return nil
	})
}

// SetNotificationInvestigation records taskID as id's investigation,
// unless one is already recorded; it returns whichever is recorded after.
func (s *Store) SetNotificationInvestigation(ctx context.Context, id int64, taskID string) (string, error) {
	var recorded string
	err := s.write(ctx, fmt.Sprintf("investigate notification %d", id), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE `notification` SET `investigation_task_id` = ? "+
			"WHERE `id` = ? AND `investigation_task_id` IS NULL", taskID, id); err != nil {
			return fmt.Errorf("recording notification %d's investigation: %w", id, err)
		}
		n, err := getNotification(ctx, tx, id)
		if err != nil {
			return err
		}
		if n != nil {
			recorded = n.InvestigationTaskID
		}
		return nil
	})
	return recorded, err
}

// SetNotificationDelivered records where id has been delivered.
func (s *Store) SetNotificationDelivered(ctx context.Context, id int64, d NotificationDelivery) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.write(ctx, fmt.Sprintf("deliver notification %d", id), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `notification` SET `delivered` = ? WHERE `id` = ?", string(b), id)
		return err
	})
}

// NotificationFilter narrows ListNotifications.
type NotificationFilter struct {
	// Open keeps only uncleared notifications.
	Open bool
	// After keeps only ids greater than it.
	After  int64
	Family string
	// RaisedBy keeps only those this principal raised.
	RaisedBy string
	// Limit is at most how many, newest first; 0 is 200.
	Limit int
}

// ListNotifications is the notifications matching f, newest first.
func (s *Store) ListNotifications(ctx context.Context, f NotificationFilter) ([]Notification, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.Open {
		where = append(where, "`cleared_at` IS NULL")
	}
	if f.After > 0 {
		where = append(where, "`id` > ?")
		args = append(args, f.After)
	}
	if f.Family != "" {
		where = append(where, "`family` = ?")
		args = append(args, f.Family)
	}
	if f.RaisedBy != "" {
		where = append(where, "`raised_by` = ?")
		args = append(args, f.RaisedBy)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	args = append(args, limit)
	out := []Notification{}
	err := each(ctx, s.db, "SELECT "+notificationColumns+" FROM `notification` WHERE "+
		strings.Join(where, " AND ")+" ORDER BY `id` DESC LIMIT ?", args, func(rows *sql.Rows) error {
		n, err := scanNotification(rows.Scan)
		if err != nil {
			return err
		}
		out = append(out, n)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing notifications: %w", err)
	}
	return out, nil
}

// SetTaskNotificationSubscriptions replaces the notification families
// taskID subscribes to.
func (s *Store) SetTaskNotificationSubscriptions(ctx context.Context, taskID string, families []string) error {
	return s.write(ctx, "subscribe task "+taskID+" to notifications", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `task_notification_subscription` WHERE `task_id` = ?", taskID); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, f := range families {
			f = strings.TrimSpace(f)
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			if _, err := tx.ExecContext(ctx, "INSERT INTO `task_notification_subscription` (`task_id`,`family`) VALUES (?,?)",
				taskID, f); err != nil {
				return fmt.Errorf("subscribing task %s to %s: %w", taskID, f, err)
			}
		}
		return nil
	})
}

// TaskNotificationSubscriptions is the families taskID subscribes to.
func (s *Store) TaskNotificationSubscriptions(ctx context.Context, taskID string) ([]string, error) {
	var out []string
	err := each(ctx, s.db, "SELECT `family` FROM `task_notification_subscription` WHERE `task_id` = ? ORDER BY `family`",
		[]any{taskID}, func(rows *sql.Rows) error {
			var f string
			if err := rows.Scan(&f); err != nil {
				return err
			}
			out = append(out, f)
			return nil
		})
	return out, err
}

// NotificationSubscribers is the tasks subscribed to family, or to a
// pattern covering it ("pipeline.*" covers "pipeline.base-red").
func (s *Store) NotificationSubscribers(ctx context.Context, family string) ([]string, error) {
	var out []string
	err := each(ctx, s.db, "SELECT `task_id`,`family` FROM `task_notification_subscription` ORDER BY `task_id`",
		nil, func(rows *sql.Rows) error {
			var id, f string
			if err := rows.Scan(&id, &f); err != nil {
				return err
			}
			if NotificationFamilyMatches(f, family) {
				out = append(out, id)
			}
			return nil
		})
	return out, err
}

// NotificationFamilyMatches reports whether a subscription's pattern
// covers family: equal, "*", or a "prefix.*" whose prefix family starts
// with.
func NotificationFamilyMatches(pattern, family string) bool {
	if pattern == family || pattern == "*" {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(family, prefix)
	}
	return false
}

// NotificationSink is an external channel notifications are delivered
// to (a webhook, a chat): once per instance, with retries, at or above
// a minimum severity. An interface only for now -- the first sink is its
// own task (docs/design/durable-tasks.md, DT-5 "External channels").
type NotificationSink interface {
	// Name identifies the sink in Notification.Delivered.External.
	Name() string
	Deliver(ctx context.Context, n Notification) error
}
