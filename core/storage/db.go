// Package storage keeps what PostGo knows that is not part of a
// collection's files: the history of requests and their responses, the
// cookies, the agents' audit log, the machine's settings and the UI's
// state. It is one SQLite database, through modernc.org/sqlite, which is
// pure Go.
package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultMaxBody is the most bytes of a response's body that AddHistory
// keeps.
const DefaultMaxBody = 10 << 20

// DB is the database. Its methods are safe from any goroutine.
type DB struct {
	db *sql.DB
	// MaxBody is the most bytes of a body AddHistory keeps; longer ones
	// are cut and marked Truncated. DefaultMaxBody unless changed.
	MaxBody int

	now func() time.Time

	mu   sync.Mutex
	jars map[string]*Jar
}

// migrations are the schema's versions; the database's user_version is
// how many it has applied. Append new ones; never edit applied ones.
var migrations = []string{
	`CREATE TABLE history (
		id INTEGER PRIMARY KEY,
		time INTEGER NOT NULL,
		collection_id TEXT NOT NULL,
		request_id TEXT NOT NULL,
		source INTEGER NOT NULL,
		client TEXT NOT NULL,
		method TEXT NOT NULL,
		url TEXT NOT NULL,
		status INTEGER NOT NULL,
		duration INTEGER NOT NULL,
		size INTEGER NOT NULL,
		err TEXT NOT NULL
	);
	CREATE INDEX history_request ON history (collection_id, request_id, id);
	CREATE INDEX history_time ON history (time);
	CREATE TABLE responses (
		history_id INTEGER PRIMARY KEY REFERENCES history (id) ON DELETE CASCADE,
		request BLOB,
		headers BLOB,
		body BLOB,
		truncated INTEGER NOT NULL,
		timing BLOB
	);
	CREATE TABLE cookies (
		collection_id TEXT NOT NULL,
		domain TEXT NOT NULL,
		path TEXT NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		host_only INTEGER NOT NULL,
		secure INTEGER NOT NULL,
		http_only INTEGER NOT NULL,
		same_site INTEGER NOT NULL,
		persistent INTEGER NOT NULL,
		expires INTEGER NOT NULL,
		creation INTEGER NOT NULL,
		last_access INTEGER NOT NULL,
		PRIMARY KEY (collection_id, domain, path, name)
	);
	CREATE TABLE audit (
		id INTEGER PRIMARY KEY,
		time INTEGER NOT NULL,
		collection_id TEXT NOT NULL,
		client TEXT NOT NULL,
		tool TEXT NOT NULL,
		args TEXT NOT NULL,
		result TEXT NOT NULL
	);
	CREATE INDEX audit_time ON audit (time);
	CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE state (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,

	// Schema 2: the full-text index of history, and the indexes of the
	// queries that filter by collection or source. The trigram tokenizer
	// makes MATCH find any substring, as LIKE does, in time that does not
	// grow with the table.
	`CREATE VIRTUAL TABLE history_text USING fts5 (
		method, url,
		content = 'history', content_rowid = 'id', tokenize = 'trigram'
	);
	INSERT INTO history_text (rowid, method, url) SELECT id, method, url FROM history;
	CREATE TRIGGER history_text_insert AFTER INSERT ON history BEGIN
		INSERT INTO history_text (rowid, method, url) VALUES (new.id, new.method, new.url);
	END;
	CREATE TRIGGER history_text_delete AFTER DELETE ON history BEGIN
		INSERT INTO history_text (history_text, rowid, method, url) VALUES ('delete', old.id, old.method, old.url);
	END;
	CREATE TRIGGER history_text_update AFTER UPDATE ON history BEGIN
		INSERT INTO history_text (history_text, rowid, method, url) VALUES ('delete', old.id, old.method, old.url);
		INSERT INTO history_text (rowid, method, url) VALUES (new.id, new.method, new.url);
	END;
	CREATE INDEX history_collection ON history (collection_id, id);
	CREATE INDEX history_source ON history (source, id);`,
}

// Open opens the database at path, making it when there is none, and
// brings its schema up to date. ":memory:" is a database in memory.
func Open(path string) (*DB, error) {
	// The driver takes the part after the first ? as parameters.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // every connection would be another database
	}
	d := &DB{db: db, MaxBody: DefaultMaxBody, now: time.Now, jars: map[string]*Jar{}}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) migrate() error {
	var version int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("storage: the database is of a newer PostGo (schema %d, this one knows %d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		err := d.tx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return err
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("storage: migrating to schema %d: %w", i+1, err)
		}
	}
	return nil
}

func (d *DB) tx(fn func(*sql.Tx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

func getJSON(db *sql.DB, table, key string, v any) (bool, error) {
	var s string
	err := db.QueryRow("SELECT value FROM "+table+" WHERE key = ?", key).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(s), v)
}

func setJSON(db *sql.DB, table, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO "+table+" (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, string(b))
	return err
}

// GetSetting decodes the JSON of a setting of the machine into v, and
// reports whether there was one.
func (d *DB) GetSetting(key string, v any) (bool, error) { return getJSON(d.db, "settings", key, v) }

// SetSetting stores v, as JSON, as a setting of the machine.
func (d *DB) SetSetting(key string, v any) error { return setJSON(d.db, "settings", key, v) }

// GetState decodes the JSON of a piece of the UI's state into v, and
// reports whether there was one.
func (d *DB) GetState(key string, v any) (bool, error) { return getJSON(d.db, "state", key, v) }

// SetState stores v, as JSON, as a piece of the UI's state.
func (d *DB) SetState(key string, v any) error { return setJSON(d.db, "state", key, v) }
