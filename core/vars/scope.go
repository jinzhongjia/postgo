package vars

import (
	"maps"
	"slices"

	"github.com/jinzhongjia/postgo/core/model"
)

// Level is the level of a layer of variables: a later level overrides an
// earlier one.
type Level int

const (
	Global      Level = iota
	Collection        // the collection's root
	Environment       // the chosen environment
	Folder            // one per enclosing folder, outermost first
	Request           // the request's own [vars]
	Data              // the row of a runner's data file
	Local             // set by scripts while the request runs
)

func (l Level) String() string {
	return [...]string{"global", "collection", "environment", "folder", "request", "data", "local"}[l]
}

// Layer is the variables of one level.
type Layer struct {
	Level Level
	// Name tells which of its level the layer is: an environment's ID, a
	// folder's path.
	Name string
	// Vars are the variables; disabled ones are skipped, and of two
	// enabled ones of the same name the later wins.
	Vars []model.KV
	// Secrets are the names of variables whose values the SecretLookup
	// of a Resolver gives. A secret wins over a variable of the same
	// name in its layer.
	Secrets []string
}

// Change is a value Set changed.
type Change struct {
	Level Level
	Name  string // the layer's Name
	Var   string
	Value string
	// Secret is set when the variable is a secret, whose new value
	// belongs in the credential store rather than a file.
	Secret bool
}

// Scope is the variables of every level that apply to a request. Its
// methods are not safe for concurrent use.
type Scope struct {
	layers  []*layer
	changes []Change
}

type layer struct {
	Layer
	secrets map[string]bool
	// set holds the values Set gave, which win over the layer's own,
	// secrets included.
	set map[string]string
}

// NewScope returns a scope of the layers, which it copies: Set changes
// the scope, not them. Layers are kept in the order of their levels;
// layers of one level keep the order given.
func NewScope(layers ...Layer) *Scope {
	s := &Scope{}
	for _, l := range layers {
		s.insert(l)
	}
	return s
}

func (s *Scope) insert(l Layer) *layer {
	nl := &layer{Layer: Layer{Level: l.Level, Name: l.Name, Vars: slices.Clone(l.Vars), Secrets: slices.Clone(l.Secrets)}}
	nl.secrets = map[string]bool{}
	for _, name := range l.Secrets {
		nl.secrets[name] = true
	}
	i := len(s.layers)
	for i > 0 && s.layers[i-1].Level > l.Level {
		i--
	}
	s.layers = slices.Insert(s.layers, i, nl)
	return nl
}

// BuildInput is what Build makes a scope of.
type BuildInput struct {
	Globals    []model.KV
	Collection *model.Collection
	// Parents are the folders enclosing the request, the root first, as
	// Collection.Find returns them; the root's variables are those of
	// the collection.
	Parents []*model.Folder
	Request *model.Request
	Env     *model.Environment // nil without an environment
	Data    map[string]string
}

// Build returns the scope of a request: globals, the collection's root,
// the environment, the enclosing folders, the request, the data row.
func Build(in BuildInput) *Scope {
	s := NewScope(Layer{Level: Global, Vars: in.Globals})
	if in.Collection != nil && in.Collection.Root != nil {
		s.insert(Layer{Level: Collection, Name: in.Collection.ID, Vars: in.Collection.Root.Config.Vars})
	}
	if e := in.Env; e != nil {
		var secrets []string
		for _, sec := range e.Secrets {
			if !sec.Disabled {
				secrets = append(secrets, sec.Name)
			}
		}
		s.insert(Layer{Level: Environment, Name: e.ID, Vars: e.Vars, Secrets: secrets})
	}
	for _, f := range in.Parents {
		if f != nil && f.Path != "" {
			s.insert(Layer{Level: Folder, Name: f.Path, Vars: f.Config.Vars})
		}
	}
	if r := in.Request; r != nil {
		s.insert(Layer{Level: Request, Name: r.ID, Vars: r.Vars})
	}
	if len(in.Data) > 0 {
		var kvs []model.KV
		for _, k := range slices.Sorted(maps.Keys(in.Data)) {
			kvs = append(kvs, model.KV{Key: k, Value: in.Data[k]})
		}
		s.insert(Layer{Level: Data, Vars: kvs})
	}
	return s
}

// entry is what a name refers to in a scope.
type entry struct {
	value  string
	secret bool // the value is unknown until looked up, unless set
	set    bool // the value came from Set
	layer  *layer
}

// find returns what name refers to: the last layer that has it.
func (s *Scope) find(name string) (entry, bool) {
	for i := len(s.layers) - 1; i >= 0; i-- {
		l := s.layers[i]
		if v, ok := l.set[name]; ok {
			return entry{value: v, secret: l.secrets[name], set: true, layer: l}, true
		}
		if l.secrets[name] {
			return entry{secret: true, layer: l}, true
		}
		for j := len(l.Vars) - 1; j >= 0; j-- {
			if kv := l.Vars[j]; !kv.Disabled && kv.Key == name {
				return entry{value: kv.Value, layer: l}, true
			}
		}
	}
	return entry{}, false
}

// Set gives a variable a value in the last layer of the level, making the
// layer when there is none. The value wins over what the layer had; a
// secret stays a secret.
func (s *Scope) Set(level Level, name, value string) {
	var l *layer
	for i := len(s.layers) - 1; i >= 0; i-- {
		if s.layers[i].Level == level {
			l = s.layers[i]
			break
		}
	}
	if l == nil {
		l = s.insert(Layer{Level: level})
	}
	if l.set == nil {
		l.set = map[string]string{}
	}
	l.set[name] = value
	c := Change{Level: level, Name: l.Name, Var: name, Value: value, Secret: l.secrets[name]}
	for i, old := range s.changes {
		if old.Level == c.Level && old.Name == c.Name && old.Var == c.Var {
			s.changes[i] = c
			return
		}
	}
	s.changes = append(s.changes, c)
}

// Changes returns the values Set changed, one per variable of a layer,
// in the order first set.
func (s *Scope) Changes() []Change { return slices.Clone(s.changes) }

// Names returns every variable the scope defines, sorted, for completion.
func (s *Scope) Names() []string {
	seen := map[string]bool{}
	for _, l := range s.layers {
		for _, kv := range l.Vars {
			if !kv.Disabled {
				seen[kv.Key] = true
			}
		}
		for n := range l.secrets {
			seen[n] = true
		}
		for n := range l.set {
			seen[n] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}
