package model

import (
	"reflect"
	"testing"
	"time"
)

func TestEnabled(t *testing.T) {
	kvs := []KV{{Key: "a"}, {Key: "b", Disabled: true}, {Key: "c"}}
	got := Enabled(kvs)
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "c" {
		t.Errorf("Enabled = %v", got)
	}
	if got := Enabled(nil); got == nil || len(got) != 0 {
		t.Errorf("Enabled(nil) = %#v, want empty", got)
	}
}

func TestOpt(t *testing.T) {
	var unset Opt[int]
	if unset.Or(3) != 3 || Some(5).Or(3) != 5 {
		t.Error("Or")
	}
	if Some(1).Override(unset) != Some(1) || Some(1).Override(Some(2)) != Some(2) || unset.Override(unset) != unset {
		t.Error("Override")
	}
}

func TestSettingsOverride(t *testing.T) {
	child := Settings{Timeout: Some(5 * time.Second), VerifyTLS: Some(false)}
	got := Defaults.Override(child)
	want := Defaults
	want.Timeout, want.VerifyTLS = Some(5*time.Second), Some(false)
	if got != want {
		t.Errorf("Override = %+v, want %+v", got, want)
	}
	// Every default is set, so that settings resolve without gaps.
	v := reflect.ValueOf(Defaults)
	for i := 0; i < v.NumField(); i++ {
		if !v.Field(i).FieldByName("Set").Bool() {
			t.Errorf("Defaults.%s is not set", v.Type().Field(i).Name)
		}
	}
}

func TestBodyType(t *testing.T) {
	raw := map[BodyType]bool{BodyJSON: true, BodyText: true, BodyXML: true, BodyHTML: true, BodyGraphQL: true}
	for _, bt := range BodyTypes {
		if bt.IsRaw() != raw[bt] {
			t.Errorf("%s.IsRaw() = %v", bt, bt.IsRaw())
		}
	}
	if BodyJSON.ContentType() != "application/json" || BodyForm.ContentType() != "application/x-www-form-urlencoded" ||
		BodyMultipart.ContentType() != "" || BodyNone.ContentType() != "" || BodyXML.ContentType() != "application/xml" ||
		BodyText.ContentType() != "text/plain" || BodyHTML.ContentType() != "text/html" || BodyGraphQL.ContentType() != "application/json" {
		t.Error("ContentType")
	}
}

func TestScriptsEmpty(t *testing.T) {
	if !(Scripts{}).Empty() || (Scripts{Tests: "x"}).Empty() {
		t.Error("Empty")
	}
}

func TestNewRequestDefaults(t *testing.T) {
	cases := map[Kind]struct {
		method string
		body   BodyType
		proto  ProtoSource
	}{
		KindHTTP:    {"GET", BodyNone, ""},
		KindGraphQL: {"POST", BodyGraphQL, ""},
		KindWS:      {"", BodyNone, ""},
		KindSSE:     {"", BodyNone, ""},
		KindGRPC:    {"", BodyNone, ProtoReflection},
	}
	for kind, want := range cases {
		r := NewRequest(kind)
		if r.Kind != kind || r.Method != want.method || r.Body.Type != want.body || r.Proto.Source != want.proto || r.Auth.Type != AuthInherit {
			t.Errorf("NewRequest(%s) = %+v", kind, r)
		}
	}
}

func sampleCollection() *Collection {
	c := NewCollection("3f6c2a1e-8d4b-4c1f-9a77-1b2e5d0c9e10", "My API")
	users := NewFolder("users")
	list := NewRequest(KindHTTP)
	list.ID, list.URL = "users/list.postgo", "{{baseUrl}}/users"
	create := NewRequest(KindHTTP)
	create.ID, create.Method, create.URL = "users/create.postgo", "POST", "{{baseUrl}}/users"
	users.Children = []Node{{Request: list}, {Request: create}}
	health := NewRequest(KindHTTP)
	health.ID, health.URL = "health.postgo", "{{baseUrl}}/health"
	c.Root.Children = []Node{{Folder: users}, {Request: health}}
	c.Envs = []*Environment{NewEnvironment("dev", "Development")}
	c.DefaultEnv = "dev"
	return c
}

func TestWalkOrder(t *testing.T) {
	c := sampleCollection()
	var got []string
	var depths []int
	c.Root.Walk(func(parents []*Folder, n Node) bool {
		if n.Folder != nil {
			got = append(got, "folder:"+n.Folder.Path)
		} else {
			got = append(got, n.Request.ID)
		}
		depths = append(depths, len(parents))
		return true
	})
	want := []string{"folder:", "folder:users", "users/list.postgo", "users/create.postgo", "health.postgo"}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(depths, []int{0, 1, 2, 2, 1}) {
		t.Errorf("Walk = %v %v", got, depths)
	}
	n := 0
	c.Root.Walk(func([]*Folder, Node) bool { n++; return n < 3 })
	if n != 3 {
		t.Errorf("Walk went on after false: %d calls", n)
	}
}

func TestFind(t *testing.T) {
	c := sampleCollection()
	r, parents := c.Find("users/create.postgo")
	if r == nil || r.Method != "POST" || len(parents) != 2 || parents[0] != c.Root || parents[1].Path != "users" {
		t.Errorf("Find = %v %v", r, parents)
	}
	if r, _ := c.Find("nope.postgo"); r != nil {
		t.Error("Find found a missing request")
	}
	if c.Env("dev") == nil || c.Env("prod") != nil {
		t.Error("Env")
	}
}
