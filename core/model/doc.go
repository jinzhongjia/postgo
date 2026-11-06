// Package model holds PostGo's domain types: requests, folders,
// collections and environments.
//
// The types are independent of the file format: package store maps them
// to and from .postgo files (docs/FORMAT.md). Every other package talks to
// the others through these types, so they are frozen once reviewed; later
// changes may only add fields.
//
// Values are plain data. Enum fields have no implicit default: the zero
// value "" is invalid, and NewRequest, NewCollection and NewEnvironment
// return values with every default filled in. Validate reports what a
// value breaks; Clone returns a copy that shares no memory with the
// original.
//
// Paths (request IDs, folder paths, file references) use forward slashes
// and are relative to the collection's root.
package model
