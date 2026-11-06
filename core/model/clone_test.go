package model

import (
	"reflect"
	"testing"
)

// fill sets every field reachable from v to a value that is not zero:
// slices get two elements, pointers a new value. depth bounds recursive
// types such as Folder.
func fill(v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i), depth)
		}
	case reflect.Slice:
		if depth <= 0 {
			return
		}
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i), depth-1)
		}
		v.Set(s)
	case reflect.Pointer:
		if depth <= 0 {
			return
		}
		p := reflect.New(v.Type().Elem())
		fill(p.Elem(), depth-1)
		v.Set(p)
	}
}

// shared reports the path of memory a and b share: a slice's backing
// array or a pointer's target.
func shared(a, b reflect.Value, path string) string {
	switch a.Kind() {
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if p := shared(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); p != "" {
				return p
			}
		}
	case reflect.Slice:
		if a.Len() > 0 && a.Pointer() == b.Pointer() {
			return path
		}
		for i := 0; i < a.Len(); i++ {
			if p := shared(a.Index(i), b.Index(i), path+"[]"); p != "" {
				return p
			}
		}
	case reflect.Pointer:
		if a.IsNil() {
			return ""
		}
		if a.Pointer() == b.Pointer() {
			return path
		}
		return shared(a.Elem(), b.Elem(), path)
	}
	return ""
}

func checkClone[T any](t *testing.T, name string, orig *T, clone func(*T) *T) {
	t.Helper()
	fill(reflect.ValueOf(orig).Elem(), 4)
	c := clone(orig)
	if !reflect.DeepEqual(orig, c) {
		t.Errorf("%s: clone differs from the original", name)
	}
	if p := shared(reflect.ValueOf(orig), reflect.ValueOf(c), name); p != "" {
		t.Errorf("%s: clone shares %s with the original", name, p)
	}
}

func TestCloneSharesNothing(t *testing.T) {
	checkClone(t, "Request", &Request{}, (*Request).Clone)
	checkClone(t, "Environment", &Environment{}, (*Environment).Clone)
	checkClone(t, "Folder", &Folder{}, (*Folder).Clone)
	checkClone(t, "Collection", &Collection{}, (*Collection).Clone)
}

func TestCloneNil(t *testing.T) {
	if (*Request)(nil).Clone() != nil || (*Folder)(nil).Clone() != nil ||
		(*Environment)(nil).Clone() != nil || (*Collection)(nil).Clone() != nil {
		t.Error("Clone of nil is not nil")
	}
}

func TestCloneKeepsNilSlices(t *testing.T) {
	c := (&Collection{Root: &Folder{}}).Clone()
	if c.Envs != nil || c.Root.Children != nil {
		t.Error("Clone made nil slices empty")
	}
}
