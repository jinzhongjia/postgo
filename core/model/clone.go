package model

import "slices"

// Clone returns a copy of r that shares no memory with it.
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	c := *r
	c.Tags = slices.Clone(r.Tags)
	c.Query = slices.Clone(r.Query)
	c.Path = slices.Clone(r.Path)
	c.Headers = slices.Clone(r.Headers)
	c.Body = r.Body.Clone()
	c.Auth = r.Auth.Clone()
	c.Vars = slices.Clone(r.Vars)
	c.Asserts = slices.Clone(r.Asserts)
	c.Messages = slices.Clone(r.Messages)
	c.Metadata = slices.Clone(r.Metadata)
	c.Examples = slices.Clone(r.Examples)
	return &c
}

// Clone returns a copy of b that shares no memory with it.
func (b Body) Clone() Body {
	b.Form = slices.Clone(b.Form)
	b.Parts = slices.Clone(b.Parts)
	return b
}

// Clone returns a copy of a that shares no memory with it.
func (a Auth) Clone() Auth {
	a.OAuth2.AuthParams = slices.Clone(a.OAuth2.AuthParams)
	a.OAuth2.TokenParams = slices.Clone(a.OAuth2.TokenParams)
	return a
}

// Clone returns a copy of c that shares no memory with it.
func (c Config) Clone() Config {
	c.Headers = slices.Clone(c.Headers)
	c.Auth = c.Auth.Clone()
	c.Vars = slices.Clone(c.Vars)
	return c
}

// Clone returns a deep copy of f, its folders and requests included.
func (f *Folder) Clone() *Folder {
	if f == nil {
		return nil
	}
	c := &Folder{Path: f.Path, Config: f.Config.Clone()}
	if f.Children != nil {
		c.Children = make([]Node, len(f.Children))
		for i, n := range f.Children {
			c.Children[i] = Node{Folder: n.Folder.Clone(), Request: n.Request.Clone()}
		}
	}
	return c
}

// Clone returns a copy of e that shares no memory with it.
func (e *Environment) Clone() *Environment {
	if e == nil {
		return nil
	}
	c := *e
	c.Vars = slices.Clone(e.Vars)
	c.Secrets = slices.Clone(e.Secrets)
	return &c
}

// Clone returns a deep copy of c.
func (c *Collection) Clone() *Collection {
	if c == nil {
		return nil
	}
	d := *c
	d.Root = c.Root.Clone()
	if c.Envs != nil {
		d.Envs = make([]*Environment, len(c.Envs))
		for i, e := range c.Envs {
			d.Envs[i] = e.Clone()
		}
	}
	return &d
}
