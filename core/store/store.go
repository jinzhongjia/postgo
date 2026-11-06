package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/jinzhongjia/postgo/core/model"
	"github.com/jinzhongjia/postgo/core/store/pgfile"
)

var (
	// ErrNotCollection is the error of opening a directory without a
	// _collection.postgo.
	ErrNotCollection = errors.New("store: not a collection: there is no _collection.postgo")
	// ErrConflict is the error of a write whose base is not the file's
	// version: someone else changed the file since it was read.
	ErrConflict = errors.New("store: the file changed since it was read")
	// ErrExists is the error of creating what is already there.
	ErrExists = errors.New("store: it already exists")
	// ErrNotFound is the error of changing what is not there.
	ErrNotFound = errors.New("store: not found")
	// ErrNotEmpty is the error of deleting a folder with requests or
	// folders in it.
	ErrNotEmpty = errors.New("store: the folder is not empty")
)

// ValidationError is the error of a write of a value with errors.
type ValidationError struct {
	Issues model.Issues
}

func (e *ValidationError) Error() string { return "store: " + e.Issues.Err().Error() }

// EventKind is the kind of an Event.
type EventKind int

const (
	Changed EventKind = iota
	Created
	Deleted
	Renamed
	Problem // the file could not be read
)

func (k EventKind) String() string {
	return [...]string{"changed", "created", "deleted", "renamed", "problem"}[k]
}

// Event is a change of a collection's file.
type Event struct {
	Kind    EventKind
	Path    string
	OldPath string // of Renamed
	// External is set for changes made outside the store: by git, an
	// editor or another PostGo.
	External bool
}

// Watcher tells of changes to files under a directory. The app gives one
// over mygo.Watch; changed may be called from any goroutine, with paths
// absolute or relative to dir.
type Watcher interface {
	Watch(dir string, changed func(paths []string)) (stop func(), err error)
}

// Options configure Open.
type Options struct {
	// Watcher, when not nil, keeps the store up to date with changes made
	// outside it.
	Watcher Watcher
}

// Store is a collection's directory. Its methods are safe from any
// goroutine; events are delivered after the change, outside the store's
// lock, on the goroutine that made it.
type Store struct {
	dir string

	mu   sync.Mutex
	snap *snapshot
	subs map[int]func(Event)
	next int
	stop func()
}

// Open opens the collection in dir.
func Open(dir string, o Options) (*Store, error) {
	snap, err := scan(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, snap: snap, subs: map[int]func(Event){}}
	if o.Watcher != nil {
		stop, err := o.Watcher.Watch(dir, func([]string) { s.reload() })
		if err != nil {
			return nil, err
		}
		s.stop = stop
	}
	return s, nil
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Create makes a new collection in dir, which is made when missing: its
// _collection.postgo, with a new ID, and a .gitignore leaving the local
// overrides out of git.
func Create(dir, name string) (*Store, error) {
	c := model.NewCollection(newUUID(), name)
	if is := c.Validate(); is.HasErrors() {
		return nil, &ValidationError{is}
	}
	if _, err := os.Stat(filepath.Join(dir, collectionFile)); err == nil {
		return nil, ErrExists
	}
	ci := pgfile.CollectionInfo{ID: c.ID, Name: name, Config: c.Root.Config}
	if err := writeAtomic(filepath.Join(dir, collectionFile), pgfile.WriteCollection(ci, nil)); err != nil {
		return nil, err
	}
	gi := filepath.Join(dir, ".gitignore")
	old, err := os.ReadFile(gi)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if !slices.Contains(strings.Fields(string(old)), localDir+"/") {
		if len(old) > 0 && !strings.HasSuffix(string(old), "\n") {
			old = append(old, '\n')
		}
		if err := writeAtomic(gi, append(old, localDir+"/\n"...)); err != nil {
			return nil, err
		}
	}
	return Open(dir, Options{})
}

// Close stops watching the directory.
func (s *Store) Close() error {
	s.mu.Lock()
	stop := s.stop
	s.stop = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

// Dir returns the collection's directory.
func (s *Store) Dir() string { return s.dir }

// Collection returns a copy of the collection.
func (s *Store) Collection() *model.Collection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap.coll.Clone()
}

// Problems returns the files that could not be read.
func (s *Store) Problems() []FileProblem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.snap.problems)
}

// Version returns the version of a file, by its path in the collection,
// as the store last read or wrote it; "" when it has none.
func (s *Store) Version(rel string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.snap.files[rel]; f != nil {
		return f.version
	}
	return ""
}

// LocalOverrides returns the variables .postgo.local/ overrides in an
// environment.
func (s *Store) LocalOverrides(envID string) []model.KV {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.snap.locals[envID])
}

// Subscribe calls fn for each event until cancel is called.
func (s *Store) Subscribe(fn func(Event)) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.next
	s.next++
	s.subs[id] = fn
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// change runs fn under the store's lock, then delivers the events it
// returns.
func (s *Store) change(fn func() ([]Event, error)) error {
	s.mu.Lock()
	evs, err := fn()
	subs := make([]func(Event), 0, len(s.subs))
	for _, id := range slices.Sorted(maps.Keys(s.subs)) {
		subs = append(subs, s.subs[id])
	}
	s.mu.Unlock()
	for _, e := range evs {
		for _, fn := range subs {
			fn(e)
		}
	}
	return err
}
