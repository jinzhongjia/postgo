package storage

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Source is who sent a request.
type Source int

const (
	SourceAny   Source = iota // in queries: either
	SourceUser                // the user, from the UI or the CLI
	SourceAgent               // an AI agent, through MCP
)

// Entry is a request in the history.
type Entry struct {
	ID           int64
	Time         time.Time
	CollectionID string
	RequestID    string // "" for a request not saved in the collection
	Source       Source
	Client       string // the agent's client, as "claude-code"
	Method       string
	URL          string // resolved, its secrets masked
	Status       int    // 0 when no response came
	Duration     time.Duration
	Size         int64
	Err          string
}

// StoredResponse is what the history keeps of an exchange, its secrets
// masked by the caller.
type StoredResponse struct {
	Request   []byte // the request as sent
	Headers   []byte
	Body      []byte
	Truncated bool   // Body was cut at DB.MaxBody
	Timing    []byte // JSON
}

// HistoryQuery selects entries. Zero fields select everything.
type HistoryQuery struct {
	CollectionID string
	RequestID    string
	Text         string // in the method or the URL, ignoring case
	Source       Source
	Since        time.Time
	Before       int64 // entries older than this ID: the cursor of the next page
	Limit        int   // 100 when 0; Delete ignores it
}

func (q HistoryQuery) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, arg any) {
		conds = append(conds, cond)
		args = append(args, arg)
	}
	if q.CollectionID != "" {
		add("collection_id = ?", q.CollectionID)
	}
	if q.RequestID != "" {
		add("request_id = ?", q.RequestID)
	}
	if q.Text != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q.Text)
		conds = append(conds, `(method || ' ' || url) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+esc+"%")
	}
	if q.Source != SourceAny {
		add("source = ?", int(q.Source))
	}
	if !q.Since.IsZero() {
		add("time >= ?", q.Since.UnixNano())
	}
	if q.Before > 0 {
		add("id < ?", q.Before)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// AddHistory adds an entry, with its response when r is not nil, and
// returns its ID. A body longer than MaxBody is cut and marked Truncated.
// A zero Time is the current time.
func (d *DB) AddHistory(e Entry, r *StoredResponse) (int64, error) {
	if e.Time.IsZero() {
		e.Time = d.now()
	}
	var id int64
	err := d.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO history (time, collection_id, request_id, source, client, method, url, status, duration, size, err)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.Time.UnixNano(), e.CollectionID, e.RequestID, int(e.Source), e.Client, e.Method, e.URL, e.Status, int64(e.Duration), e.Size, e.Err)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if r == nil {
			return nil
		}
		body, truncated := r.Body, r.Truncated
		if d.MaxBody > 0 && len(body) > d.MaxBody {
			body, truncated = body[:d.MaxBody], true
		}
		_, err = tx.Exec(`INSERT INTO responses (history_id, request, headers, body, truncated, timing) VALUES (?, ?, ?, ?, ?, ?)`,
			id, r.Request, r.Headers, body, truncated, r.Timing)
		return err
	})
	return id, err
}

// History returns the entries the query selects, the newest first.
func (d *DB) History(q HistoryQuery) ([]Entry, error) {
	where, args := q.where()
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.db.Query(`SELECT id, time, collection_id, request_id, source, client, method, url, status, duration, size, err
		FROM history`+where+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var t, dur int64
		var src int
		if err := rows.Scan(&e.ID, &t, &e.CollectionID, &e.RequestID, &src, &e.Client, &e.Method, &e.URL, &e.Status, &dur, &e.Size, &e.Err); err != nil {
			return nil, err
		}
		e.Time, e.Duration, e.Source = time.Unix(0, t), time.Duration(dur), Source(src)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrNotFound is the error of lookups of what is not there.
var ErrNotFound = errors.New("storage: not found")

// Response returns the response of an entry, or ErrNotFound.
func (d *DB) Response(id int64) (*StoredResponse, error) {
	r := &StoredResponse{}
	err := d.db.QueryRow(`SELECT request, headers, body, truncated, timing FROM responses WHERE history_id = ?`, id).
		Scan(&r.Request, &r.Headers, &r.Body, &r.Truncated, &r.Timing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DeleteHistory deletes the entries the query selects, ignoring its
// Limit, with their responses, and returns how many.
func (d *DB) DeleteHistory(q HistoryQuery) (int, error) {
	where, args := q.where()
	res, err := d.db.Exec("DELETE FROM history"+where, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// PruneHistory deletes the entries older than olderThan, when it is
// positive, and those after the newest keep, when keep is positive, and
// returns how many.
func (d *DB) PruneHistory(keep int, olderThan time.Duration) (int, error) {
	total := 0
	err := d.tx(func(tx *sql.Tx) error {
		if olderThan > 0 {
			res, err := tx.Exec("DELETE FROM history WHERE time < ?", d.now().Add(-olderThan).UnixNano())
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			total += int(n)
		}
		if keep > 0 {
			res, err := tx.Exec("DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY id DESC LIMIT ?)", keep)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			total += int(n)
		}
		return nil
	})
	return total, err
}

// RenameRequest moves the history of a request to its new ID, as when the
// request's file moved.
func (d *DB) RenameRequest(collectionID, oldID, newID string) error {
	_, err := d.db.Exec("UPDATE history SET request_id = ? WHERE collection_id = ? AND request_id = ?", newID, collectionID, oldID)
	return err
}

// AuditEntry is a tool an agent called.
type AuditEntry struct {
	ID           int64
	Time         time.Time
	CollectionID string
	Client       string
	Tool         string
	Args         string // a summary of the arguments, secrets masked
	Result       string // ok, denied, error: …
}

// AuditQuery selects audit entries. Zero fields select everything.
type AuditQuery struct {
	CollectionID string
	Client       string
	Tool         string
	Since        time.Time
	Before       int64
	Limit        int // 100 when 0
}

// AppendAudit adds an audit entry. A zero Time is the current time.
func (d *DB) AppendAudit(a AuditEntry) error {
	if a.Time.IsZero() {
		a.Time = d.now()
	}
	_, err := d.db.Exec(`INSERT INTO audit (time, collection_id, client, tool, args, result) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Time.UnixNano(), a.CollectionID, a.Client, a.Tool, a.Args, a.Result)
	return err
}

// Audit returns the audit entries the query selects, the newest first.
func (d *DB) Audit(q AuditQuery) ([]AuditEntry, error) {
	var conds []string
	var args []any
	for _, c := range []struct {
		col, val string
	}{{"collection_id", q.CollectionID}, {"client", q.Client}, {"tool", q.Tool}} {
		if c.val != "" {
			conds = append(conds, c.col+" = ?")
			args = append(args, c.val)
		}
	}
	if !q.Since.IsZero() {
		conds = append(conds, "time >= ?")
		args = append(args, q.Since.UnixNano())
	}
	if q.Before > 0 {
		conds = append(conds, "id < ?")
		args = append(args, q.Before)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.db.Query("SELECT id, time, collection_id, client, tool, args, result FROM audit"+where+" ORDER BY id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var t int64
		if err := rows.Scan(&a.ID, &t, &a.CollectionID, &a.Client, &a.Tool, &a.Args, &a.Result); err != nil {
			return nil, err
		}
		a.Time = time.Unix(0, t)
		out = append(out, a)
	}
	return out, rows.Err()
}
