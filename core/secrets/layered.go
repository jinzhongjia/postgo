package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// ErrUnreadable is the error of Get when a value kept in a file cannot be
// read back: the file is missing or was changed, or the key that
// encrypted it is gone from the backend.
var ErrUnreadable = errors.New("secrets: the stored value cannot be read")

// DefaultMaxInline is the largest value New keeps in the backend itself,
// below mygo.MaxSecretSize (2560 bytes) with room to spare.
const DefaultMaxInline = 2048

const (
	masterKeyName = "postgo:internal:master-key"
	fileMagic     = "PGS1"
)

// fileMarker is the backend's value of a key whose value is in a file. It
// starts with a NUL byte, which no text secret does; values that start
// with it anyway go to a file too, so they are never mistaken for it.
var fileMarker = []byte("\x00postgo-file:v1")

// Options configure New.
type Options struct {
	// Backend keeps small values, and the key that encrypts large ones:
	// the system's credential store.
	Backend Store
	// Dir is the directory large values are written to, encrypted, as
	// "<user data>/secrets". It is made when first needed.
	Dir string
	// MaxInline is the largest value kept in Backend, DefaultMaxInline
	// when 0.
	MaxInline int
}

// Layered is a Store of values of any size over a backend that keeps
// small ones: values up to MaxInline bytes go to the backend; larger ones
// are encrypted with AES-256-GCM, under a key the backend keeps, into a
// file of Dir, and the backend keeps a marker for them. Keys longer than
// MaxKey are kept under their hash.
type Layered struct {
	backend   Store
	dir       string
	maxInline int

	mu  sync.Mutex // guards key, and orders the steps of Set and Delete
	key []byte
}

// New returns a Layered store.
func New(o Options) (*Layered, error) {
	if o.Backend == nil {
		return nil, errors.New("secrets: Options.Backend is nil")
	}
	if o.Dir == "" {
		return nil, errors.New("secrets: Options.Dir is empty")
	}
	if o.MaxInline <= 0 {
		o.MaxInline = DefaultMaxInline
	}
	return &Layered{backend: o.Backend, dir: o.Dir, maxInline: o.MaxInline}, nil
}

func (l *Layered) file(bk string) string {
	sum := sha256.Sum256([]byte(bk))
	return filepath.Join(l.dir, hex.EncodeToString(sum[:])+".secret")
}

func checkKey(key string) error {
	if key == "" {
		return errors.New("secrets: empty key")
	}
	if isInternal(key) {
		return fmt.Errorf("secrets: %q is reserved", key)
	}
	return nil
}

// Get implements Store.
func (l *Layered) Get(key string) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	bk := backendKey(key)
	v, err := l.backend.Get(bk)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(v, fileMarker) {
		return v, nil
	}
	data, err := os.ReadFile(l.file(bk))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: its file is missing", ErrUnreadable)
	}
	if err != nil {
		return nil, err
	}
	mk, err := l.masterKey(false)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: its encryption key is gone", ErrUnreadable)
	}
	if err != nil {
		return nil, err
	}
	return open(mk, key, data)
}

// Set implements Store.
func (l *Layered) Set(key string, value []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	bk := backendKey(key)
	if len(value) <= l.maxInline && !bytes.HasPrefix(value, fileMarker[:1]) {
		if err := l.backend.Set(bk, value); err != nil {
			return err
		}
		return removeFile(l.file(bk))
	}
	mk, err := l.masterKey(true)
	if err != nil {
		return err
	}
	data, err := seal(mk, key, value)
	if err != nil {
		return err
	}
	if err := writeFile(l.dir, l.file(bk), data); err != nil {
		return err
	}
	if err := l.backend.Set(bk, fileMarker); err != nil {
		// The backend may still hold the previous value; leave the file
		// only if a marker might point to it.
		if prev, gerr := l.backend.Get(bk); gerr != nil || !bytes.Equal(prev, fileMarker) {
			removeFile(l.file(bk))
		}
		return err
	}
	return nil
}

// Delete implements Store.
func (l *Layered) Delete(key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	bk := backendKey(key)
	if err := l.backend.Delete(bk); err != nil {
		return err
	}
	return removeFile(l.file(bk))
}

// masterKey returns the key that encrypts files, making and storing it
// when create is set and the backend has none. l.mu is held.
func (l *Layered) masterKey(create bool) ([]byte, error) {
	if l.key != nil {
		return l.key, nil
	}
	k, err := l.backend.Get(masterKeyName)
	switch {
	case err == nil:
		if len(k) != 32 {
			return nil, fmt.Errorf("%w: the encryption key is damaged", ErrUnreadable)
		}
	case errors.Is(err, ErrNotFound) && create:
		k = make([]byte, 32)
		rand.Read(k)
		if err := l.backend.Set(masterKeyName, k); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	l.key = k
	return k, nil
}

// seal encrypts value, bound to key so that the file of one key does not
// decrypt as another's.
func seal(mk []byte, key string, value []byte) ([]byte, error) {
	aead, err := newAEAD(mk)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(fileMagic)+aead.NonceSize(), len(fileMagic)+aead.NonceSize()+len(value)+aead.Overhead())
	copy(out, fileMagic)
	rand.Read(out[len(fileMagic):])
	return aead.Seal(out, out[len(fileMagic):], value, []byte(key)), nil
}

func open(mk []byte, key string, data []byte) ([]byte, error) {
	aead, err := newAEAD(mk)
	if err != nil {
		return nil, err
	}
	n := len(fileMagic) + aead.NonceSize()
	if len(data) < n || string(data[:len(fileMagic)]) != fileMagic {
		return nil, fmt.Errorf("%w: its file is damaged", ErrUnreadable)
	}
	v, err := aead.Open(nil, data[len(fileMagic):n], data[n:], []byte(key))
	if err != nil {
		return nil, fmt.Errorf("%w: its file was changed or belongs to another key", ErrUnreadable)
	}
	return v, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// writeFile writes data to name atomically: readers see the old file or
// the new one, never part of it.
func writeFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func removeFile(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
