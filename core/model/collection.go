package model

// FormatVersion is the version of the file format this model maps to.
const FormatVersion = 1

// Config is what a folder, or the collection's root, gives the requests
// inside it. Headers and Vars merge level by level, the inner winning;
// Auth (unless inherit) and Settings override; Scripts run from the
// collection down to the request.
type Config struct {
	Headers  []KV
	Auth     Auth
	Vars     []KV
	Scripts  Scripts
	Settings Settings
	Docs     string
}

// Folder is a directory of a collection. Its children are in the order
// the user arranged them.
type Folder struct {
	// Path is the folder's path relative to the collection's root, "" for
	// the root. Its last element is the folder's name.
	Path     string
	Config   Config
	Children []Node
}

// NewFolder returns an empty folder at the path, inheriting its auth.
func NewFolder(path string) *Folder {
	return &Folder{Path: path, Config: Config{Auth: Auth{Type: AuthInherit}}}
}

// Node is a child of a folder: exactly one of Folder and Request is set.
type Node struct {
	Folder  *Folder
	Request *Request
}

// Collection is a collection: its root folder and its environments.
type Collection struct {
	// ID is a UUID made when the collection is created, which stays the
	// same wherever its directory is.
	ID         string
	Name       string
	DefaultEnv string // the ID of an environment, or ""
	Root       *Folder
	Envs       []*Environment
}

// NewCollection returns an empty collection with the ID and name. Its
// root has no auth, as there is nothing for it to inherit.
func NewCollection(id, name string) *Collection {
	return &Collection{
		ID:   id,
		Name: name,
		Root: &Folder{Config: Config{Auth: Auth{Type: AuthNone}}},
	}
}

// Walk calls fn for every folder and request under f, f first, then its
// children in order, depth first. parents are the folders enclosing the
// node, outermost first, f included for its children. Walk stops when fn
// returns false.
func (f *Folder) Walk(fn func(parents []*Folder, n Node) bool) {
	f.walk(nil, fn)
}

func (f *Folder) walk(parents []*Folder, fn func([]*Folder, Node) bool) bool {
	if !fn(parents, Node{Folder: f}) {
		return false
	}
	parents = append(parents[:len(parents):len(parents)], f)
	for _, c := range f.Children {
		switch {
		case c.Folder != nil:
			if !c.Folder.walk(parents, fn) {
				return false
			}
		case c.Request != nil:
			if !fn(parents, c) {
				return false
			}
		}
	}
	return true
}

// Find returns the request with the ID and the folders enclosing it,
// outermost (the root) first, or nil.
func (c *Collection) Find(id string) (*Request, []*Folder) {
	var req *Request
	var path []*Folder
	c.Root.Walk(func(parents []*Folder, n Node) bool {
		if n.Request != nil && n.Request.ID == id {
			req, path = n.Request, parents
			return false
		}
		return true
	})
	return req, path
}

// Env returns the environment with the ID, or nil.
func (c *Collection) Env(id string) *Environment {
	for _, e := range c.Envs {
		if e.ID == id {
			return e
		}
	}
	return nil
}
