package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestKey(t *testing.T) {
	if got := Key("c1", "dev", "token"); got != "postgo:c1:dev:token" {
		t.Errorf("Key = %q", got)
	}
}

func TestMemory(t *testing.T) {
	var m Memory
	if _, err := m.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: %v", err)
	}
	if err := m.Delete("a"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
	v := []byte("one")
	m.Set("a", v)
	v[0] = 'X' // the store keeps its own copy
	got, err := m.Get("a")
	if err != nil || string(got) != "one" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	got[0] = 'Y'
	if again, _ := m.Get("a"); string(again) != "one" {
		t.Error("Get returned the store's own slice")
	}
	m.Set("b", nil)
	if k := m.Keys(); len(k) != 2 || k[0] != "a" || k[1] != "b" {
		t.Errorf("Keys = %v", k)
	}
	m.Delete("a")
	if _, err := m.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get deleted: %v", err)
	}
}

func newLayered(t *testing.T, backend Store, dir string) *Layered {
	t.Helper()
	l, err := New(Options{Backend: backend, Dir: dir, MaxInline: 16})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.secret"))
	return m
}

func TestNewOptions(t *testing.T) {
	if _, err := New(Options{Dir: "x"}); err == nil {
		t.Error("New without a backend")
	}
	if _, err := New(Options{Backend: &Memory{}}); err == nil {
		t.Error("New without a directory")
	}
	l, err := New(Options{Backend: &Memory{}, Dir: "x"})
	if err != nil || l.maxInline != DefaultMaxInline {
		t.Errorf("New default MaxInline = %v, %v", l, err)
	}
}

func TestLayeredSmallAndLarge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	backend := &Memory{}
	l := newLayered(t, backend, dir)

	small := []byte("short")
	if err := l.Set("k", small); err != nil {
		t.Fatal(err)
	}
	if v, _ := backend.Get("k"); !bytes.Equal(v, small) {
		t.Errorf("small value not in the backend: %q", v)
	}
	if len(files(t, dir)) != 0 {
		t.Error("small value wrote a file")
	}

	large := bytes.Repeat([]byte("0123456789"), 100)
	if err := l.Set("k", large); err != nil {
		t.Fatal(err)
	}
	if v, _ := backend.Get("k"); !bytes.Equal(v, fileMarker) {
		t.Errorf("backend holds %q, want the marker", v)
	}
	fs := files(t, dir)
	if len(fs) != 1 {
		t.Fatalf("files = %v", fs)
	}
	raw, _ := os.ReadFile(fs[0])
	if bytes.Contains(raw, large[:20]) {
		t.Error("the file holds the value in clear")
	}
	if got, err := l.Get("k"); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("Get large = %d bytes, %v", len(got), err)
	}

	// A new store over the same backend and directory reads it back.
	if got, err := newLayered(t, backend, dir).Get("k"); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("Get from a new store: %v", err)
	}

	// Going back to a small value removes the file.
	if err := l.Set("k", small); err != nil {
		t.Fatal(err)
	}
	if len(files(t, dir)) != 0 {
		t.Error("file left after a small value replaced a large one")
	}
	if got, _ := l.Get("k"); !bytes.Equal(got, small) {
		t.Errorf("Get = %q", got)
	}

	l.Set("k", large)
	if err := l.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if len(files(t, dir)) != 0 {
		t.Error("Delete left the file")
	}
	if _, err := l.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get deleted: %v", err)
	}
	if err := l.Delete("k"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func TestLayeredMarkerLikeValue(t *testing.T) {
	l := newLayered(t, &Memory{}, t.TempDir())
	for _, v := range [][]byte{fileMarker, []byte("\x00x")} {
		if err := l.Set("k", v); err != nil {
			t.Fatal(err)
		}
		if got, err := l.Get("k"); err != nil || !bytes.Equal(got, v) {
			t.Errorf("Get %q = %q, %v", v, got, err)
		}
	}
}

func TestLayeredEmptyValue(t *testing.T) {
	l := newLayered(t, &Memory{}, t.TempDir())
	l.Set("k", nil)
	if got, err := l.Get("k"); err != nil || len(got) != 0 {
		t.Errorf("Get empty = %q, %v", got, err)
	}
}

func TestLayeredLongKey(t *testing.T) {
	backend := &Memory{}
	l := newLayered(t, backend, t.TempDir())
	long := "postgo:" + strings.Repeat("x", 300)
	short := []byte("v")
	if err := l.Set(long, short); err != nil {
		t.Fatal(err)
	}
	for _, k := range backend.Keys() {
		if len(k) > MaxKey {
			t.Errorf("backend got a key of %d bytes", len(k))
		}
	}
	if got, err := l.Get(long); err != nil || !bytes.Equal(got, short) {
		t.Errorf("Get = %q, %v", got, err)
	}
}

func TestLayeredReservedKeys(t *testing.T) {
	l := newLayered(t, &Memory{}, t.TempDir())
	for _, k := range []string{"", masterKeyName} {
		if l.Set(k, []byte("x")) == nil {
			t.Errorf("Set(%q) accepted", k)
		}
		if _, err := l.Get(k); err == nil {
			t.Errorf("Get(%q) accepted", k)
		}
		if l.Delete(k) == nil {
			t.Errorf("Delete(%q) accepted", k)
		}
	}
}

func TestLayeredUnreadable(t *testing.T) {
	large := bytes.Repeat([]byte("a"), 100)
	setup := func(t *testing.T) (*Memory, string, *Layered) {
		backend, dir := &Memory{}, t.TempDir()
		l := newLayered(t, backend, dir)
		if err := l.Set("k", large); err != nil {
			t.Fatal(err)
		}
		return backend, dir, l
	}
	t.Run("missing file", func(t *testing.T) {
		_, dir, l := setup(t)
		os.Remove(files(t, dir)[0])
		if _, err := l.Get("k"); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Get = %v", err)
		}
	})
	t.Run("changed file", func(t *testing.T) {
		_, dir, l := setup(t)
		f := files(t, dir)[0]
		data, _ := os.ReadFile(f)
		data[len(data)-1] ^= 1
		os.WriteFile(f, data, 0o600)
		if _, err := l.Get("k"); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Get = %v", err)
		}
	})
	t.Run("truncated file", func(t *testing.T) {
		_, dir, l := setup(t)
		os.WriteFile(files(t, dir)[0], []byte("PG"), 0o600)
		if _, err := l.Get("k"); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Get = %v", err)
		}
	})
	t.Run("another key's file", func(t *testing.T) {
		_, _, l := setup(t)
		l.Set("other", bytes.Repeat([]byte("b"), 100))
		data, _ := os.ReadFile(l.file("other"))
		os.WriteFile(l.file("k"), data, 0o600)
		if _, err := l.Get("k"); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Get = %v", err)
		}
	})
	t.Run("lost master key", func(t *testing.T) {
		backend, dir, _ := setup(t)
		backend.Delete(masterKeyName)
		if _, err := newLayered(t, backend, dir).Get("k"); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Get = %v", err)
		}
	})
	t.Run("damaged master key", func(t *testing.T) {
		backend, dir, _ := setup(t)
		backend.Set(masterKeyName, []byte("short"))
		if _, err := newLayered(t, backend, dir).Get("k"); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Get = %v", err)
		}
	})
}

// failing is a backend whose operations on keys starting with fail fail.
type failing struct {
	Memory
	fail string
	err  error
}

func (f *failing) Get(k string) ([]byte, error) {
	if strings.HasPrefix(k, f.fail) {
		return nil, f.err
	}
	return f.Memory.Get(k)
}

func (f *failing) Set(k string, v []byte) error {
	if strings.HasPrefix(k, f.fail) {
		return f.err
	}
	return f.Memory.Set(k, v)
}

func (f *failing) Delete(k string) error {
	if strings.HasPrefix(k, f.fail) {
		return f.err
	}
	return f.Memory.Delete(k)
}

func TestLayeredBackendErrors(t *testing.T) {
	boom := errors.New("keyring locked")
	large := bytes.Repeat([]byte("a"), 100)

	b := &failing{fail: "k", err: boom}
	dir := t.TempDir()
	l := newLayered(t, b, dir)
	if err := l.Set("k", []byte("x")); !errors.Is(err, boom) {
		t.Errorf("Set small = %v", err)
	}
	if err := l.Set("k", large); !errors.Is(err, boom) {
		t.Errorf("Set large = %v", err)
	}
	if len(files(t, dir)) != 0 {
		t.Error("a failed Set left a file")
	}
	if _, err := l.Get("k"); !errors.Is(err, boom) {
		t.Errorf("Get = %v", err)
	}
	if err := l.Delete("k"); !errors.Is(err, boom) {
		t.Errorf("Delete = %v", err)
	}

	mk := &failing{fail: masterKeyName, err: boom}
	l = newLayered(t, mk, t.TempDir())
	if err := l.Set("k", large); !errors.Is(err, boom) {
		t.Errorf("Set without a master key = %v", err)
	}

	// The master key cannot be stored.
	sf := &failing{fail: "postgo:internal", err: ErrNotFound}
	l = newLayered(t, sf, t.TempDir())
	if err := l.Set("k", large); err == nil {
		t.Error("Set succeeded without storing the master key")
	}

	// A large value whose marker cannot be stored over an earlier large
	// value keeps that value readable.
	m := &Memory{}
	dir = t.TempDir()
	good := newLayered(t, m, dir)
	good.Set("k", large)
	b2 := &markerFails{Memory: m, err: boom}
	l = newLayered(t, b2, dir)
	if err := l.Set("k", bytes.Repeat([]byte("b"), 100)); !errors.Is(err, boom) {
		t.Errorf("Set = %v", err)
	}
	if len(files(t, dir)) != 1 {
		t.Error("the earlier value's file is gone")
	}
}

// markerFails fails to store the file marker.
type markerFails struct {
	*Memory
	err error
}

func (m *markerFails) Set(k string, v []byte) error {
	if bytes.Equal(v, fileMarker) {
		return m.err
	}
	return m.Memory.Set(k, v)
}

func TestLayeredDirIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o600)
	l := newLayered(t, &Memory{}, f)
	if err := l.Set("k", bytes.Repeat([]byte("a"), 100)); err == nil {
		t.Error("Set into a file as a directory succeeded")
	}
}

func TestLayeredConcurrent(t *testing.T) {
	l := newLayered(t, &Memory{}, t.TempDir())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := fmt.Sprint("k", i%3)
			for j := range 20 {
				v := bytes.Repeat([]byte{byte('a' + i)}, (j%2)*100+1)
				if err := l.Set(k, v); err != nil {
					t.Error(err)
					return
				}
				if _, err := l.Get(k); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
