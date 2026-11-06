package model

// Environment is a set of variables chosen as a whole, as dev or prod.
type Environment struct {
	// ID is the environment's file name without its extension, as "dev".
	ID    string
	Name  string
	Color string
	Vars  []KV
	// Secrets are the names of variables whose values the system's
	// credential store keeps, outside the collection's files.
	Secrets []Secret
}

// NewEnvironment returns an empty environment.
func NewEnvironment(id, name string) *Environment {
	return &Environment{ID: id, Name: name}
}

// Secret is a variable whose value is kept in the credential store.
type Secret struct {
	Name     string
	Desc     string
	Disabled bool
}
