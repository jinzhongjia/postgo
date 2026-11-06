package model

// KV is one row of a key-value list: a header, a query or path parameter,
// a form field, a variable.
//
// Disabled rows are kept but not sent; Desc is the row's description,
// which may span several lines.
type KV struct {
	Key      string
	Value    string
	Desc     string
	Disabled bool
}

// Enabled returns the rows of kvs that are not disabled, in order.
func Enabled(kvs []KV) []KV {
	out := make([]KV, 0, len(kvs))
	for _, kv := range kvs {
		if !kv.Disabled {
			out = append(out, kv)
		}
	}
	return out
}

// Assert is one declarative assertion, such as "res.status == 201".
type Assert struct {
	Expr     string
	Desc     string
	Disabled bool
}

// Opt is an optional setting: Set is false when the value is inherited
// from the enclosing folder or collection.
type Opt[T comparable] struct {
	Set   bool
	Value T
}

// Some returns an Opt holding v.
func Some[T comparable](v T) Opt[T] { return Opt[T]{Set: true, Value: v} }

// Or returns the option's value, or def when it is not set.
func (o Opt[T]) Or(def T) T {
	if o.Set {
		return o.Value
	}
	return def
}

// Override returns child when it is set, else o.
func (o Opt[T]) Override(child Opt[T]) Opt[T] {
	if child.Set {
		return child
	}
	return o
}
