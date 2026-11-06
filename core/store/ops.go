package store

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jinzhongjia/postgo/core/model"
	"github.com/jinzhongjia/postgo/core/store/pgfile"
)

// writeAtomic writes data to name so that readers see the old file or the
// new one, never part of either.
func writeAtomic(name string, data []byte) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".postgo-*")
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
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

type reader func(rel string, data []byte) (*pgfile.Origin, error)

func readRequest(rel string, data []byte) (*pgfile.Origin, error) {
	_, o, err := pgfile.ReadRequest(rel, data)
	return o, err
}

func readFolder(_ string, data []byte) (*pgfile.Origin, error) {
	_, _, o, err := pgfile.ReadFolder(data)
	return o, err
}

func readCollection(_ string, data []byte) (*pgfile.Origin, error) {
	_, o, err := pgfile.ReadCollection(data)
	return o, err
}

func readEnv(rel string, data []byte) (*pgfile.Origin, error) {
	_, o, err := pgfile.ReadEnvironment(strings.TrimSuffix(path.Base(rel), ext), data)
	return o, err
}

// prepare reads a file before it is written: when check is set, its
// version must be base ("" for a file that must not exist). It returns
// the Origin to write with and whether the file exists. s.mu is held.
func (s *Store) prepare(rel, base string, check bool, read reader) (*pgfile.Origin, bool, error) {
	cur, err := os.ReadFile(abs(s.dir, rel))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if check && base != "" {
			return nil, false, ErrConflict
		}
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	v := version(cur)
	if check {
		if base == "" {
			return nil, true, ErrExists
		}
		if v != base {
			return nil, true, ErrConflict
		}
	}
	if f := s.snap.files[rel]; f != nil && f.version == v && f.origin != nil {
		return f.origin, true, nil
	}
	o, _ := read(rel, cur) // a file that does not read is written anew
	return o, true, nil
}

// commit writes a file and remembers its version and Origin. s.mu is held.
func (s *Store) commit(rel string, data []byte, read reader) (string, error) {
	if err := writeAtomic(abs(s.dir, rel), data); err != nil {
		return "", err
	}
	o, _ := read(rel, data)
	v := version(data)
	s.snap.files[rel] = &fileState{v, o}
	s.snap.problems = slices.DeleteFunc(s.snap.problems, func(p FileProblem) bool { return p.Path == rel })
	return v, nil
}

// folder returns the folder at the path, "" for the root, or nil.
func (s *Store) folder(p string) *model.Folder {
	var found *model.Folder
	s.snap.coll.Root.Walk(func(_ []*model.Folder, n model.Node) bool {
		if n.Folder != nil && n.Folder.Path == p {
			found = n.Folder
			return false
		}
		return true
	})
	return found
}

func parentOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

func childIndex(f *model.Folder, name string) int {
	return slices.IndexFunc(f.Children, func(n model.Node) bool { return nodeName(n) == name })
}

// clash reports whether a child other than self has the name, ignoring
// case, as file systems of macOS and Windows do.
func clash(f *model.Folder, name, self string) bool {
	return slices.ContainsFunc(f.Children, func(n model.Node) bool {
		c := nodeName(n)
		return c != self && strings.EqualFold(c, name)
	})
}

func idIssues(id string) model.Issues {
	r := model.NewRequest(model.KindHTTP)
	r.ID, r.URL = id, "x"
	var out model.Issues
	for _, i := range r.Validate() {
		if i.Field == "id" && i.Severity == model.Error {
			out = append(out, i)
		}
	}
	if id == "" {
		out = append(out, model.Issue{Field: "id", Severity: model.Error, Code: model.CodeRequired, Msg: "requests in a collection have an ID"})
	}
	return out
}

func folderIssues(p string) model.Issues {
	is := idIssues(p + "/x" + ext)
	for _, e := range strings.Split(p, "/") {
		if strings.HasPrefix(e, "_") || strings.HasPrefix(e, ".") {
			is = append(is, model.Issue{Field: "path", Severity: model.Error, Code: model.CodeReserved, Msg: "folder names must not start with _ or ."})
		}
	}
	return is
}

// SaveRequest writes a request at its ID. base is the version of its file
// as read, "" for a new request.
func (s *Store) SaveRequest(r *model.Request, base string) (string, error) {
	var v string
	err := s.change(func() ([]Event, error) {
		r := r.Clone()
		if is := idIssues(r.ID); len(is) > 0 {
			return nil, &ValidationError{is}
		}
		if is := r.Validate(); is.HasErrors() {
			return nil, &ValidationError{is}
		}
		f := s.folder(parentOf(r.ID))
		if f == nil {
			return nil, fmt.Errorf("%w: folder %q", ErrNotFound, parentOf(r.ID))
		}
		name := path.Base(r.ID)
		i := childIndex(f, name)
		if i < 0 && clash(f, name, "") {
			return nil, ErrExists
		}
		o, existed, err := s.prepare(r.ID, base, true, readRequest)
		if err != nil {
			return nil, err
		}
		if v, err = s.commit(r.ID, pgfile.WriteRequest(r, o), readRequest); err != nil {
			return nil, err
		}
		if i >= 0 {
			f.Children[i].Request = r
		} else {
			f.Children = arrange(append(f.Children, model.Node{Request: r}), s.snap.orders[f.Path])
		}
		kind := Changed
		if !existed {
			kind = Created
		}
		return []Event{{Kind: kind, Path: r.ID}}, nil
	})
	return v, err
}

// DeleteRequest deletes a request's file.
func (s *Store) DeleteRequest(id string) error {
	return s.change(func() ([]Event, error) {
		f := s.folder(parentOf(id))
		if f == nil {
			return nil, ErrNotFound
		}
		i := childIndex(f, path.Base(id))
		_, known := s.snap.files[id]
		if (i < 0 || f.Children[i].Request == nil) && !known {
			return nil, ErrNotFound
		}
		if err := os.Remove(abs(s.dir, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		delete(s.snap.files, id)
		s.snap.problems = slices.DeleteFunc(s.snap.problems, func(p FileProblem) bool { return p.Path == id })
		if i >= 0 && f.Children[i].Request != nil {
			f.Children = slices.Delete(f.Children, i, i+1)
		}
		evs := []Event{{Kind: Deleted, Path: id}}
		ev, err := s.dropFromOrder(f.Path, path.Base(id))
		return append(evs, ev...), err
	})
}

// MoveRequest moves a request's file to the new ID, in its folder or
// another.
func (s *Store) MoveRequest(id, newID string) error {
	return s.change(func() ([]Event, error) {
		if is := idIssues(newID); len(is) > 0 {
			return nil, &ValidationError{is}
		}
		from := s.folder(parentOf(id))
		if from == nil {
			return nil, ErrNotFound
		}
		i := childIndex(from, path.Base(id))
		if i < 0 || from.Children[i].Request == nil {
			return nil, ErrNotFound
		}
		to := s.folder(parentOf(newID))
		if to == nil {
			return nil, fmt.Errorf("%w: folder %q", ErrNotFound, parentOf(newID))
		}
		self := ""
		if to == from {
			self = path.Base(id)
		}
		if newID == id {
			return nil, nil
		}
		if clash(to, path.Base(newID), self) {
			return nil, ErrExists
		}
		if _, err := os.Lstat(abs(s.dir, newID)); err == nil && !strings.EqualFold(id, newID) {
			return nil, ErrExists
		}
		if err := os.Rename(abs(s.dir, id), abs(s.dir, newID)); err != nil {
			return nil, err
		}
		r := from.Children[i].Request
		from.Children = slices.Delete(from.Children, i, i+1)
		r.ID = newID
		to.Children = arrange(append(to.Children, model.Node{Request: r}), s.snap.orders[to.Path])
		if st := s.snap.files[id]; st != nil {
			delete(s.snap.files, id)
			s.snap.files[newID] = st
		}
		evs := []Event{{Kind: Renamed, Path: newID, OldPath: id}}
		if from != to || path.Base(id) != path.Base(newID) {
			ev, err := s.dropFromOrder(from.Path, path.Base(id))
			evs = append(evs, ev...)
			if err != nil {
				return evs, err
			}
		}
		return evs, nil
	})
}

// CreateFolder makes a folder: its directory and an empty _folder.postgo,
// which marks it a folder while it has no requests.
func (s *Store) CreateFolder(p string) error {
	return s.change(func() ([]Event, error) {
		if is := folderIssues(p); len(is) > 0 {
			return nil, &ValidationError{is}
		}
		parent := s.folder(parentOf(p))
		if parent == nil {
			return nil, fmt.Errorf("%w: folder %q", ErrNotFound, parentOf(p))
		}
		if childIndex(parent, path.Base(p)) >= 0 || clash(parent, path.Base(p), "") {
			return nil, ErrExists
		}
		rel := p + "/" + folderFile
		if _, err := os.Lstat(abs(s.dir, rel)); err == nil {
			return nil, ErrExists
		}
		if _, err := s.commit(rel, nil, readFolder); err != nil {
			return nil, err
		}
		parent.Children = arrange(append(parent.Children, model.Node{Folder: model.NewFolder(p)}), s.snap.orders[parent.Path])
		return []Event{{Kind: Created, Path: p}}, nil
	})
}

// MoveFolder moves a folder, and what is in it, to the new path.
func (s *Store) MoveFolder(p, np string) error {
	return s.change(func() ([]Event, error) {
		if p == "" {
			return nil, errors.New("store: the root does not move")
		}
		if is := folderIssues(np); len(is) > 0 {
			return nil, &ValidationError{is}
		}
		if np == p || strings.HasPrefix(np, p+"/") {
			return nil, errors.New("store: a folder does not move into itself")
		}
		from := s.folder(parentOf(p))
		f := s.folder(p)
		to := s.folder(parentOf(np))
		if f == nil || from == nil {
			return nil, ErrNotFound
		}
		if to == nil {
			return nil, fmt.Errorf("%w: folder %q", ErrNotFound, parentOf(np))
		}
		self := ""
		if to == from {
			self = path.Base(p)
		}
		if clash(to, path.Base(np), self) {
			return nil, ErrExists
		}
		if _, err := os.Lstat(abs(s.dir, np)); err == nil && !strings.EqualFold(p, np) {
			return nil, ErrExists
		}
		if err := os.Rename(abs(s.dir, p), abs(s.dir, np)); err != nil {
			return nil, err
		}
		from.Children = slices.Delete(from.Children, childIndex(from, path.Base(p)), childIndex(from, path.Base(p))+1)
		evs := []Event{{Kind: Renamed, Path: np, OldPath: p}}
		var move func(f *model.Folder, newPath string)
		move = func(f *model.Folder, newPath string) {
			old := f.Path
			f.Path = newPath
			if o, ok := s.snap.orders[old]; ok {
				delete(s.snap.orders, old)
				s.snap.orders[newPath] = o
			}
			for _, c := range f.Children {
				if c.Folder != nil {
					oldPath := c.Folder.Path
					move(c.Folder, newPath+"/"+path.Base(oldPath))
					evs = append(evs, Event{Kind: Renamed, Path: c.Folder.Path, OldPath: oldPath})
				} else {
					oldID := c.Request.ID
					c.Request.ID = newPath + "/" + path.Base(oldID)
					evs = append(evs, Event{Kind: Renamed, Path: c.Request.ID, OldPath: oldID})
				}
			}
		}
		move(f, np)
		for rel, st := range s.snap.files {
			if strings.HasPrefix(rel, p+"/") {
				delete(s.snap.files, rel)
				s.snap.files[np+rel[len(p):]] = st
			}
		}
		to.Children = arrange(append(to.Children, model.Node{Folder: f}), s.snap.orders[to.Path])
		if from != to || path.Base(p) != path.Base(np) {
			ev, err := s.dropFromOrder(from.Path, path.Base(p))
			evs = append(evs, ev...)
			if err != nil {
				return evs, err
			}
		}
		return evs, nil
	})
}

// DeleteFolder deletes an empty folder: its _folder.postgo, and its
// directory unless other files are in it.
func (s *Store) DeleteFolder(p string) error {
	return s.change(func() ([]Event, error) {
		f := s.folder(p)
		parent := s.folder(parentOf(p))
		if f == nil || p == "" || parent == nil {
			return nil, ErrNotFound
		}
		if len(f.Children) > 0 {
			return nil, ErrNotEmpty
		}
		rel := p + "/" + folderFile
		if err := os.Remove(abs(s.dir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		os.Remove(abs(s.dir, p)) // fails, as it should, when other files are there
		delete(s.snap.files, rel)
		delete(s.snap.orders, p)
		i := childIndex(parent, path.Base(p))
		parent.Children = slices.Delete(parent.Children, i, i+1)
		evs := []Event{{Kind: Deleted, Path: p}}
		ev, err := s.dropFromOrder(parent.Path, path.Base(p))
		return append(evs, ev...), err
	})
}

// configIssues validates the configuration of a folder, or of the root.
func configIssues(cfg model.Config, root bool) model.Issues {
	c := model.NewCollection("00000000-0000-4000-8000-000000000000", "x")
	prefix := "root.config"
	if root {
		c.Root.Config = cfg
	} else {
		f := model.NewFolder("x")
		f.Config = cfg
		c.Root.Children = []model.Node{{Folder: f}}
		prefix = "root.children[0].config"
	}
	var out model.Issues
	for _, i := range c.Validate() {
		if strings.HasPrefix(i.Field, prefix) {
			i.Field = strings.TrimPrefix(strings.TrimPrefix(i.Field, prefix), ".")
			out = append(out, i)
		}
	}
	return out
}

// SaveFolder writes a folder's configuration. base is the version of its
// _folder.postgo as read.
func (s *Store) SaveFolder(p string, cfg model.Config, base string) (string, error) {
	var v string
	err := s.change(func() ([]Event, error) {
		if p == "" {
			return nil, errors.New("store: the root's configuration is saved with SaveCollectionInfo")
		}
		f := s.folder(p)
		if f == nil {
			return nil, ErrNotFound
		}
		if is := configIssues(cfg, false); is.HasErrors() {
			return nil, &ValidationError{is}
		}
		rel := p + "/" + folderFile
		o, existed, err := s.prepare(rel, base, true, readFolder)
		if err != nil {
			return nil, err
		}
		cfg := cfg.Clone()
		if v, err = s.commit(rel, pgfile.WriteFolder(cfg, s.snap.orders[p], o), readFolder); err != nil {
			return nil, err
		}
		f.Config = cfg
		kind := Changed
		if !existed {
			kind = Created
		}
		return []Event{{Kind: kind, Path: rel}}, nil
	})
	return v, err
}

// SetOrder sets the order of a folder's children, "" for the root's, by
// name; children it does not name come after, by name.
func (s *Store) SetOrder(folder string, names []string) error {
	return s.change(func() ([]Event, error) {
		f := s.folder(folder)
		if f == nil {
			return nil, ErrNotFound
		}
		seen := map[string]bool{}
		for _, n := range names {
			if childIndex(f, n) < 0 {
				return nil, fmt.Errorf("%w: %q is not in the folder", ErrNotFound, n)
			}
			if seen[n] {
				return nil, fmt.Errorf("store: %q is named twice", n)
			}
			seen[n] = true
		}
		return s.writeOrder(folder, slices.Clone(names))
	})
}

// dropFromOrder takes a name out of a folder's order, if it is there.
func (s *Store) dropFromOrder(folder, name string) ([]Event, error) {
	order := s.snap.orders[folder]
	if !slices.Contains(order, name) {
		return nil, nil
	}
	return s.writeOrder(folder, slices.DeleteFunc(slices.Clone(order), func(n string) bool { return n == name }))
}

// writeOrder writes a folder's order into its file, keeping the rest of
// the file as it is on disk. s.mu is held.
func (s *Store) writeOrder(folder string, order []string) ([]Event, error) {
	if len(order) == 0 {
		order = nil
	}
	f := s.folder(folder)
	var rel string
	var data []byte
	var read reader
	if folder == "" {
		rel, read = collectionFile, readCollection
		cur, err := os.ReadFile(abs(s.dir, rel))
		if err != nil {
			return nil, err
		}
		ci, o, err := pgfile.ReadCollection(cur)
		if err != nil {
			return nil, err
		}
		ci.Order = order
		data = pgfile.WriteCollection(ci, o)
		s.snap.info.Order = order
	} else {
		rel, read = folder+"/"+folderFile, readFolder
		cfg, o := f.Config, (*pgfile.Origin)(nil)
		if cur, err := os.ReadFile(abs(s.dir, rel)); err == nil {
			if cfg, _, o, err = pgfile.ReadFolder(cur); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		data = pgfile.WriteFolder(cfg, order, o)
	}
	if _, err := s.commit(rel, data, read); err != nil {
		return nil, err
	}
	s.snap.orders[folder] = order
	f.Children = arrange(f.Children, order)
	return []Event{{Kind: Changed, Path: rel}}, nil
}

// SaveCollectionInfo writes the collection's name, default environment
// and root configuration. base is the version of _collection.postgo as
// read.
func (s *Store) SaveCollectionInfo(name, defaultEnv string, cfg model.Config, base string) (string, error) {
	var v string
	err := s.change(func() ([]Event, error) {
		c := s.snap.coll.Clone()
		c.Name, c.DefaultEnv, c.Root.Config = name, defaultEnv, cfg
		var is model.Issues
		for _, i := range c.Validate() {
			if i.Field == "name" || i.Field == "defaultEnv" || strings.HasPrefix(i.Field, "root.config") {
				is = append(is, i)
			}
		}
		if is.HasErrors() {
			return nil, &ValidationError{is}
		}
		o, _, err := s.prepare(collectionFile, base, true, readCollection)
		if err != nil {
			return nil, err
		}
		ci := s.snap.info
		ci.Name, ci.DefaultEnv, ci.Config = name, defaultEnv, cfg.Clone()
		if v, err = s.commit(collectionFile, pgfile.WriteCollection(ci, o), readCollection); err != nil {
			return nil, err
		}
		s.snap.info = ci
		s.snap.coll.Name, s.snap.coll.DefaultEnv, s.snap.coll.Root.Config = name, defaultEnv, ci.Config
		return []Event{{Kind: Changed, Path: collectionFile}}, nil
	})
	return v, err
}

func envPath(id string) string   { return envDir + "/" + id + ext }
func localPath(id string) string { return localDir + "/" + envPath(id) }

// SaveEnvironment writes an environment, as it shows with its local
// overrides merged: a variable the overrides set is changed in
// .postgo.local/, the shared file keeping its own value; the others go to
// the shared file. base is the version of the shared file as read.
func (s *Store) SaveEnvironment(e *model.Environment, base string) (string, error) {
	var v string
	err := s.change(func() ([]Event, error) {
		if is := e.Validate(); is.HasErrors() {
			return nil, &ValidationError{is}
		}
		e := e.Clone()
		rel := envPath(e.ID)
		local := s.snap.locals[e.ID]
		overridden := map[string]bool{}
		for _, kv := range local {
			if !kv.Disabled {
				overridden[kv.Key] = true
			}
		}
		shared := e.Clone()
		shared.Vars = nil
		var newLocal []model.KV
		old := s.snap.shared[e.ID]
		for _, kv := range e.Vars {
			if !overridden[kv.Key] || kv.Disabled {
				shared.Vars = append(shared.Vars, kv)
				continue
			}
			newLocal = append(newLocal, model.KV{Key: kv.Key, Value: kv.Value, Desc: localDesc(local, kv.Key)})
			if old != nil {
				if i := slices.IndexFunc(old.Vars, func(o model.KV) bool { return o.Key == kv.Key && !o.Disabled }); i >= 0 {
					keep := old.Vars[i]
					keep.Desc = kv.Desc
					shared.Vars = append(shared.Vars, keep)
				}
			}
		}
		o, existed, err := s.prepare(rel, base, true, readEnv)
		if err != nil {
			return nil, err
		}
		if v, err = s.commit(rel, pgfile.WriteEnvironment(shared, o), readEnv); err != nil {
			return nil, err
		}
		kind := Changed
		if !existed {
			kind = Created
		}
		evs := []Event{{Kind: kind, Path: rel}}
		s.snap.shared[e.ID] = shared
		if !slices.Equal(newLocal, local) {
			ev, err := s.writeLocal(e.ID, newLocal)
			evs = append(evs, ev...)
			if err != nil {
				return evs, err
			}
		}
		s.setEnv(merge(shared, s.snap.locals[e.ID]))
		return evs, nil
	})
	return v, err
}

func localDesc(local []model.KV, key string) string {
	for _, kv := range local {
		if kv.Key == key {
			return kv.Desc
		}
	}
	return ""
}

// setEnv puts an environment in the collection, in the order of IDs.
func (s *Store) setEnv(e *model.Environment) {
	envs := slices.DeleteFunc(s.snap.coll.Envs, func(x *model.Environment) bool { return x.ID == e.ID })
	i, _ := slices.BinarySearchFunc(envs, e.ID, func(x *model.Environment, id string) int { return cmp.Compare(x.ID, id) })
	s.snap.coll.Envs = slices.Insert(envs, i, e)
}

// writeLocal writes an environment's local overrides, deleting the file
// when there are none. s.mu is held.
func (s *Store) writeLocal(id string, vars []model.KV) ([]Event, error) {
	rel := localPath(id)
	if len(vars) == 0 {
		if err := os.Remove(abs(s.dir, rel)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				delete(s.snap.locals, id)
				return nil, nil
			}
			return nil, err
		}
		delete(s.snap.files, rel)
		delete(s.snap.locals, id)
		return []Event{{Kind: Deleted, Path: rel}}, nil
	}
	o, existed, err := s.prepare(rel, "", false, readEnv)
	if err != nil {
		return nil, err
	}
	if _, err := s.commit(rel, pgfile.WriteEnvironment(&model.Environment{ID: id, Vars: vars}, o), readEnv); err != nil {
		return nil, err
	}
	s.snap.locals[id] = vars
	kind := Changed
	if !existed {
		kind = Created
	}
	return []Event{{Kind: kind, Path: rel}}, nil
}

// SaveLocalOverrides writes the variables of .postgo.local/ for an
// environment, which override its shared values on this machine; none
// deletes the file.
func (s *Store) SaveLocalOverrides(envID string, vars []model.KV) error {
	return s.change(func() ([]Event, error) {
		shared := s.snap.shared[envID]
		if shared == nil {
			return nil, ErrNotFound
		}
		check := &model.Environment{ID: envID, Vars: vars}
		if is := check.Validate(); is.HasErrors() {
			return nil, &ValidationError{is}
		}
		evs, err := s.writeLocal(envID, slices.Clone(vars))
		if err != nil {
			return evs, err
		}
		s.setEnv(merge(shared, s.snap.locals[envID]))
		return evs, nil
	})
}

// DeleteEnvironment deletes an environment and its local overrides.
func (s *Store) DeleteEnvironment(id string) error {
	return s.change(func() ([]Event, error) {
		if s.snap.shared[id] == nil {
			return nil, ErrNotFound
		}
		rel := envPath(id)
		if err := os.Remove(abs(s.dir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		delete(s.snap.files, rel)
		delete(s.snap.shared, id)
		evs := []Event{{Kind: Deleted, Path: rel}}
		ev, err := s.writeLocal(id, nil)
		evs = append(evs, ev...)
		s.snap.coll.Envs = slices.DeleteFunc(s.snap.coll.Envs, func(e *model.Environment) bool { return e.ID == id })
		return evs, err
	})
}

// reload reads the directory again after a change made outside the store,
// and tells what changed.
func (s *Store) reload() {
	s.change(func() ([]Event, error) {
		snap, err := scan(s.dir)
		if err != nil {
			return []Event{{Kind: Problem, Path: collectionFile, External: true}}, nil
		}
		var evs []Event
		for _, rel := range slices.Sorted(maps.Keys(snap.files)) {
			old := s.snap.files[rel]
			switch {
			case old == nil:
				evs = append(evs, Event{Kind: Created, Path: rel, External: true})
			case old.version != snap.files[rel].version:
				evs = append(evs, Event{Kind: Changed, Path: rel, External: true})
			}
		}
		for _, rel := range slices.Sorted(maps.Keys(s.snap.files)) {
			if snap.files[rel] == nil {
				evs = append(evs, Event{Kind: Deleted, Path: rel, External: true})
			}
		}
		had := map[string]string{}
		for _, p := range s.snap.problems {
			had[p.Path] = p.Err.Error()
		}
		for _, p := range snap.problems {
			if had[p.Path] != p.Err.Error() {
				evs = append(evs, Event{Kind: Problem, Path: p.Path, External: true})
			}
		}
		s.snap = snap
		return evs, nil
	})
}
