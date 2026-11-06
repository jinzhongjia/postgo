// Package secrets keeps the values of secret variables outside the
// collection's files.
//
// Store is what the rest of PostGo uses. The app gives it a backend over
// the system's credential store (mygo.Secrets, in the app layer); New
// wraps that backend so that values of any size fit, though the system's
// stores keep only small ones; Memory is a backend for tests and the CLI.
package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrNotFound is the error of Get when the key has no value.
var ErrNotFound = errors.New("secrets: not found")

// Store keeps secret values by key. Its methods are safe from any
// goroutine, and may block while the system asks the user something, as
// to unlock the keyring.
type Store interface {
	// Get returns the key's value, or ErrNotFound.
	Get(key string) ([]byte, error)
	// Set stores the value under the key, replacing any.
	Set(key string, value []byte) error
	// Delete removes the key's value; deleting a missing key is not an
	// error.
	Delete(key string) error
}

// Key returns the key of a secret variable of an environment of a
// collection: "postgo:<collection>:<env>:<name>".
func Key(collectionID, env, name string) string {
	return "postgo:" + collectionID + ":" + env + ":" + name
}

// MaxKey is the longest key backends need to keep: the Credential
// Manager's limit, the smallest of the systems'.
const MaxKey = 256

// backendKey returns the key the backend keeps a key under: the key
// itself when short enough, else a hash of it.
func backendKey(key string) string {
	if len(key) <= MaxKey {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "postgo:h:" + hex.EncodeToString(sum[:])
}

// isInternal reports whether key is one the package keeps for itself.
func isInternal(key string) bool { return strings.HasPrefix(key, "postgo:internal:") }
