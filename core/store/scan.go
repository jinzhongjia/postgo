// Package store keeps a collection's directory: it reads its files into a
// model.Collection and writes the changes back, a file at a time, with
// the checks that keep two writers from undoing each other's work.
package store

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jinzhongjia/postgo/core/model"
	"github.com/jinzhongjia/postgo/core/store/pgfile"
)

const (
	collectionFile = "_collection.postgo"
	folderFile     = "_folder.postgo"
	envDir         = "environments"
	localDir       = ".postgo.local"
	ext            = ".postgo"
)

// FileProblem is a file that could not be read; the collection goes on
// without it.
type FileProblem struct {
	Path string
	Err  error
}

// fileState is what the store knows of a file: the hash of its bytes, and
// its Origin for writing it back.
type fileState struct {
	version string
	origin  *pgfile.Origin
}

// snapshot is a collection read from its directory.
type snapshot struct {
	info     pgfile.CollectionInfo
	coll     *model.Collection // environments merged with the local overrides
	files    map[string]*fileState
	orders   map[string][]string // folder path → order, "" for the root
	shared   map[string]*model.Environment
	locals   map[string][]model.KV
	problems []FileProblem
}

func version(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

func abs(dir, rel string) string { return filepath.Join(dir, filepath.FromSlash(rel)) }

// scan reads the collection in dir.
func scan(dir string) (*snapshot, error) {
	data, err := os.ReadFile(filepath.Join(dir, collectionFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotCollection
	}
	if err != nil {
		return nil, err
	}
	info, origin, err := pgfile.ReadCollection(data)
	if err != nil {
		return nil, err
	}
	s := &snapshot{
		info:   info,
		files:  map[string]*fileState{collectionFile: {version(data), origin}},
		orders: map[string][]string{"": info.Order},
		shared: map[string]*model.Environment{},
		locals: map[string][]model.KV{},
	}
	root := &model.Folder{Config: info.Config}
	s.scanDir(dir, root)
	s.coll = &model.Collection{ID: info.ID, Name: info.Name, DefaultEnv: info.DefaultEnv, Root: root}
	s.scanEnvs(dir)
	return s, nil
}

func (s *snapshot) problem(rel string, err error) {
	s.problems = append(s.problems, FileProblem{Path: rel, Err: err})
}

// broken records a file that does not read, with its version, so that it
// can be written over to fix it.
func (s *snapshot) broken(rel string, data []byte, err error) {
	s.problem(rel, err)
	s.files[rel] = &fileState{version: version(data)}
}

// scanDir reads the requests and folders of a folder, and reports whether
// it is one: a directory with a _folder.postgo or requests under it.
func (s *snapshot) scanDir(dir string, f *model.Folder) bool {
	entries, err := os.ReadDir(abs(dir, f.Path))
	if err != nil {
		s.problem(f.Path, err)
		return false
	}
	isFolder := false
	join := func(name string) string { return path.Join(f.Path, name) }
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.Type()&fs.ModeSymlink != 0 && isDir(abs(dir, join(name))):
			continue // a link to a directory could loop
		case strings.HasPrefix(name, "."):
			continue
		case e.IsDir():
			if strings.HasPrefix(name, "_") || f.Path == "" && name == envDir {
				continue
			}
			sub := &model.Folder{Path: join(name), Config: model.Config{Auth: model.Auth{Type: model.AuthInherit}}}
			if s.scanDir(dir, sub) {
				f.Children = append(f.Children, model.Node{Folder: sub})
				isFolder = true
			}
		case name == folderFile && f.Path != "":
			isFolder = true
			rel := join(name)
			data, err := os.ReadFile(abs(dir, rel))
			if err != nil {
				s.problem(rel, err)
				continue
			}
			cfg, order, o, err := pgfile.ReadFolder(data)
			if err != nil {
				s.broken(rel, data, err)
				continue
			}
			f.Config = cfg
			s.orders[f.Path] = order
			s.files[rel] = &fileState{version(data), o}
		case strings.HasSuffix(name, ext) && !strings.HasPrefix(name, "_"):
			rel := join(name)
			data, err := os.ReadFile(abs(dir, rel))
			if err != nil {
				s.problem(rel, err)
				continue
			}
			r, o, err := pgfile.ReadRequest(rel, data)
			if err != nil {
				s.broken(rel, data, err)
				continue
			}
			f.Children = append(f.Children, model.Node{Request: r})
			s.files[rel] = &fileState{version(data), o}
			isFolder = true
		}
	}
	f.Children = arrange(f.Children, s.orders[f.Path])
	return isFolder
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func nodeName(n model.Node) string {
	if n.Folder != nil {
		return path.Base(n.Folder.Path)
	}
	return path.Base(n.Request.ID)
}

// arrange orders a folder's children: those its order names first, in
// that order, then the others by name, ignoring case.
func arrange(children []model.Node, order []string) []model.Node {
	pos := map[string]int{}
	for i, n := range order {
		if _, dup := pos[n]; !dup {
			pos[n] = i
		}
	}
	slices.SortStableFunc(children, func(a, b model.Node) int {
		an, bn := nodeName(a), nodeName(b)
		ai, aok := pos[an]
		bi, bok := pos[bn]
		switch {
		case aok && bok:
			return cmp.Compare(ai, bi)
		case aok:
			return -1
		case bok:
			return 1
		}
		return cmp.Or(cmp.Compare(strings.ToLower(an), strings.ToLower(bn)), cmp.Compare(an, bn))
	})
	return children
}

func (s *snapshot) scanEnvs(dir string) {
	entries, err := os.ReadDir(filepath.Join(dir, envDir))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.problem(envDir, err)
		}
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ext) || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		id := strings.TrimSuffix(name, ext)
		rel := envDir + "/" + name
		data, err := os.ReadFile(abs(dir, rel))
		if err != nil {
			s.problem(rel, err)
			continue
		}
		env, o, err := pgfile.ReadEnvironment(id, data)
		if err != nil {
			s.broken(rel, data, err)
			continue
		}
		s.files[rel] = &fileState{version(data), o}
		s.shared[id] = env
		lrel := localDir + "/" + rel
		if data, err := os.ReadFile(abs(dir, lrel)); err == nil {
			if local, lo, err := pgfile.ReadEnvironment(id, data); err == nil {
				s.locals[id] = local.Vars
				s.files[lrel] = &fileState{version(data), lo}
			} else {
				s.broken(lrel, data, err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			s.problem(lrel, err)
		}
		s.coll.Envs = append(s.coll.Envs, merge(env, s.locals[id]))
	}
	slices.SortFunc(s.coll.Envs, func(a, b *model.Environment) int { return cmp.Compare(a.ID, b.ID) })
}

// merge returns the environment with the local overrides: a variable the
// overrides set takes their value; one only they have is added.
func merge(env *model.Environment, local []model.KV) *model.Environment {
	m := env.Clone()
	for _, kv := range local {
		if kv.Disabled {
			continue
		}
		// The last enabled one is the one in use.
		i := len(m.Vars) - 1
		for i >= 0 && (m.Vars[i].Key != kv.Key || m.Vars[i].Disabled) {
			i--
		}
		if i >= 0 {
			m.Vars[i].Value = kv.Value
		} else {
			m.Vars = append(m.Vars, model.KV{Key: kv.Key, Value: kv.Value, Desc: kv.Desc})
		}
	}
	return m
}
