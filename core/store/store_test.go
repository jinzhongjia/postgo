package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jinzhongjia/postgo/core/model"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

const collID = "3f6c2a1e-8d4b-4c1f-9a77-1b2e5d0c9e10"

// sample writes a collection by hand, as a user or git would.
func sample(t *testing.T) string {
	dir := t.TempDir()
	write(t, dir, "_collection.postgo", "[postgo]\nformat = 1\nid = "+collID+"\nname = My API\ndefault-env = dev\n\n[order]\nusers\nhealth.postgo\n")
	write(t, dir, "health.postgo", "GET {{baseUrl}}/health\n")
	write(t, dir, "about.postgo", "GET {{baseUrl}}/about\n")
	write(t, dir, "users/_folder.postgo", "[vars]\nprefix = /v2\n\n[order]\ncreate.postgo\n")
	write(t, dir, "users/list.postgo", "GET {{baseUrl}}/users\n")
	write(t, dir, "users/create.postgo", "POST {{baseUrl}}/users\n\n[future]\nkept = yes\n")
	write(t, dir, "users/admin/ban.postgo", "DELETE {{baseUrl}}/users/:id\n\n[path]\nid = 1\n")
	write(t, dir, "fixtures/a.png", "png")
	write(t, dir, ".git/HEAD", "ref")
	write(t, dir, "_drafts/x.postgo", "GET x\n")
	write(t, dir, "broken.postgo", "[query]\nno request line\n")
	write(t, dir, "notes.txt", "not a request")
	write(t, dir, "environments/dev.postgo", "[meta]\nname = Dev\n\n[vars]\nbaseUrl = https://api.dev\nuser = shared\n\n[secrets]\ntoken\n")
	write(t, dir, "environments/prod.postgo", "[vars]\nbaseUrl = https://api\n")
	write(t, dir, ".postgo.local/environments/dev.postgo", "[vars]\nuser = me\nextra = local\n")
	return dir
}

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// tree lists a folder's nodes in order, folders with a slash.
func tree(f *model.Folder) []string {
	var out []string
	for _, n := range f.Children {
		if n.Folder != nil {
			out = append(out, n.Folder.Path+"/")
			out = append(out, tree(n.Folder)...)
		} else {
			out = append(out, n.Request.ID)
		}
	}
	return out
}

func TestOpen(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	c := s.Collection()
	if c.ID != collID || c.Name != "My API" || c.DefaultEnv != "dev" || s.Dir() != dir {
		t.Errorf("collection = %+v", c)
	}
	want := []string{"users/", "users/create.postgo", "users/admin/", "users/admin/ban.postgo", "users/list.postgo", "health.postgo", "about.postgo"}
	if got := tree(c.Root); !reflect.DeepEqual(got, want) {
		t.Errorf("tree = %v, want %v", got, want)
	}
	users := c.Root.Children[0].Folder
	if users.Config.Vars[0].Value != "/v2" || users.Config.Auth.Type != model.AuthInherit {
		t.Errorf("users config = %+v", users.Config)
	}
	if admin := users.Children[1].Folder; admin.Config.Auth.Type != model.AuthInherit {
		t.Errorf("a folder without _folder.postgo = %+v", admin.Config)
	}
	if len(c.Envs) != 2 || c.Envs[0].ID != "dev" || c.Envs[1].ID != "prod" {
		t.Fatalf("envs = %v", c.Envs)
	}
	dev := c.Envs[0]
	wantVars := []model.KV{{Key: "baseUrl", Value: "https://api.dev"}, {Key: "user", Value: "me"}, {Key: "extra", Value: "local"}}
	if !reflect.DeepEqual(dev.Vars, wantVars) || dev.Secrets[0].Name != "token" || dev.Name != "Dev" {
		t.Errorf("dev = %+v", dev)
	}
	if lo := s.LocalOverrides("dev"); len(lo) != 2 {
		t.Errorf("LocalOverrides = %v", lo)
	}
	ps := s.Problems()
	if len(ps) != 1 || ps[0].Path != "broken.postgo" {
		t.Errorf("problems = %v", ps)
	}
	if s.Version("broken.postgo") == "" || s.Version("users/list.postgo") == "" || s.Version("missing.postgo") != "" {
		t.Error("versions")
	}
	if is := c.Validate(); is.HasErrors() {
		t.Errorf("the sample is invalid: %v", is)
	}
	// The snapshot is a copy.
	c.Root.Children = nil
	if len(s.Collection().Root.Children) == 0 {
		t.Error("Collection returned the store's own")
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(t.TempDir(), Options{}); !errors.Is(err, ErrNotCollection) {
		t.Errorf("empty directory: %v", err)
	}
	dir := t.TempDir()
	write(t, dir, "_collection.postgo", "[postgo]\nformat = 9\n")
	if _, err := Open(dir, Options{}); err == nil {
		t.Error("opened a newer format")
	}
	write(t, dir, "_collection.postgo", "junk\n")
	if _, err := Open(dir, Options{}); err == nil {
		t.Error("opened a broken root file")
	}
	os.Remove(filepath.Join(dir, "_collection.postgo"))
	os.Mkdir(filepath.Join(dir, "_collection.postgo"), 0o755)
	if _, err := Open(dir, Options{}); err == nil || errors.Is(err, ErrNotCollection) {
		t.Errorf("unreadable root file: %v", err)
	}
}

func TestCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	s, err := Create(dir, "New API")
	if err != nil {
		t.Fatal(err)
	}
	c := s.Collection()
	if c.Name != "New API" || c.Validate().HasErrors() {
		t.Errorf("created %+v %v", c, c.Validate())
	}
	if got := read(t, dir, ".gitignore"); got != ".postgo.local/\n" {
		t.Errorf(".gitignore = %q", got)
	}
	if _, err := Create(dir, "Again"); !errors.Is(err, ErrExists) {
		t.Errorf("Create over a collection: %v", err)
	}
	if _, err := Create(t.TempDir(), ""); err == nil {
		t.Error("Create without a name")
	}
	other := t.TempDir()
	write(t, other, ".gitignore", "node_modules/")
	if _, err := Create(other, "x"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, other, ".gitignore"); got != "node_modules/\n.postgo.local/\n" {
		t.Errorf("appended .gitignore = %q", got)
	}
	keep := t.TempDir()
	write(t, keep, ".gitignore", "a\n.postgo.local/\n")
	Create(keep, "x")
	if got := read(t, keep, ".gitignore"); got != "a\n.postgo.local/\n" {
		t.Errorf("kept .gitignore = %q", got)
	}
}

// events records a store's events.
type events struct {
	mu  sync.Mutex
	evs []Event
}

func (e *events) add(ev Event) { e.mu.Lock(); e.evs = append(e.evs, ev); e.mu.Unlock() }
func (e *events) take() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.evs
	e.evs = nil
	return out
}

func subscribe(s *Store) *events {
	e := &events{}
	s.Subscribe(e.add)
	return e
}

func TestSaveRequest(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	ev := subscribe(s)

	r, _ := s.Collection().Find("users/create.postgo")
	r.Headers = []model.KV{{Key: "Accept", Value: "application/json"}}
	base := s.Version("users/create.postgo")
	v, err := s.SaveRequest(r, base)
	if err != nil {
		t.Fatal(err)
	}
	if v == base || s.Version("users/create.postgo") != v {
		t.Error("version not updated")
	}
	if got := read(t, dir, "users/create.postgo"); got != "POST {{baseUrl}}/users\nAccept: application/json\n\n[future]\nkept = yes\n" {
		t.Errorf("file = %q", got)
	}
	if got := ev.take(); !reflect.DeepEqual(got, []Event{{Kind: Changed, Path: "users/create.postgo"}}) {
		t.Errorf("events = %v", got)
	}
	if got, _ := s.Collection().Find("users/create.postgo"); got.Headers[0].Key != "Accept" {
		t.Error("model not updated")
	}

	// The old version is stale now.
	if _, err := s.SaveRequest(r, base); !errors.Is(err, ErrConflict) {
		t.Errorf("stale base: %v", err)
	}
	if _, err := s.SaveRequest(r, ""); !errors.Is(err, ErrExists) {
		t.Errorf("create over a file: %v", err)
	}

	n := model.NewRequest(model.KindHTTP)
	n.ID, n.URL = "users/update.postgo", "{{baseUrl}}/users/1"
	if _, err := s.SaveRequest(n, ""); err != nil {
		t.Fatal(err)
	}
	if got := tree(s.Collection().Root.Children[0].Folder); !reflect.DeepEqual(got, []string{"users/create.postgo", "users/admin/", "users/admin/ban.postgo", "users/list.postgo", "users/update.postgo"}) {
		t.Errorf("tree = %v", got)
	}
	if got := ev.take(); len(got) != 1 || got[0].Kind != Created {
		t.Errorf("events = %v", got)
	}
	n2 := n.Clone()
	n2.ID = "users/Update.postgo"
	if _, err := s.SaveRequest(n2, ""); !errors.Is(err, ErrExists) {
		t.Errorf("a name differing only in case: %v", err)
	}
	if _, err := s.SaveRequest(n, "deadbeef"); !errors.Is(err, ErrConflict) {
		t.Errorf("wrong base: %v", err)
	}
	gone := n.Clone()
	gone.ID = "users/gone.postgo"
	if _, err := s.SaveRequest(gone, "deadbeef"); !errors.Is(err, ErrConflict) {
		t.Errorf("base for a missing file: %v", err)
	}

	bad := n.Clone()
	bad.URL = ""
	var ve *ValidationError
	if _, err := s.SaveRequest(bad, s.Version(n.ID)); !errors.As(err, &ve) || !strings.Contains(err.Error(), "url") {
		t.Errorf("invalid request: %v", err)
	}
	noID := n.Clone()
	noID.ID = ""
	if _, err := s.SaveRequest(noID, ""); !errors.As(err, &ve) {
		t.Errorf("no ID: %v", err)
	}
	orphan := n.Clone()
	orphan.ID = "nowhere/x.postgo"
	if _, err := s.SaveRequest(orphan, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing folder: %v", err)
	}
}

func TestFixBrokenFile(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	r := model.NewRequest(model.KindHTTP)
	r.ID, r.URL = "broken.postgo", "https://fixed"
	if _, err := s.SaveRequest(r, s.Version("broken.postgo")); err != nil {
		t.Fatal(err)
	}
	if len(s.Problems()) != 0 {
		t.Errorf("problems = %v", s.Problems())
	}
	if got, _ := s.Collection().Find("broken.postgo"); got == nil {
		t.Error("fixed request not in the collection")
	}
}

func TestDeleteAndMoveRequests(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	ev := subscribe(s)

	if err := s.MoveRequest("users/create.postgo", "users/admin/create.postgo"); err != nil {
		t.Fatal(err)
	}
	if exists(dir, "users/create.postgo") || !exists(dir, "users/admin/create.postgo") {
		t.Error("file not moved")
	}
	if got := read(t, dir, "users/_folder.postgo"); got != "[vars]\nprefix = /v2\n" {
		t.Errorf("order kept the moved request: %q", got)
	}
	evs := ev.take()
	if len(evs) != 2 || evs[0] != (Event{Kind: Renamed, Path: "users/admin/create.postgo", OldPath: "users/create.postgo"}) || evs[1].Path != "users/_folder.postgo" {
		t.Errorf("events = %v", evs)
	}
	if r, _ := s.Collection().Find("users/admin/create.postgo"); r == nil || s.Version("users/admin/create.postgo") == "" {
		t.Error("moved request not found")
	}

	// Renaming in place, only the case.
	if err := s.MoveRequest("about.postgo", "About.postgo"); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Collection().Find("About.postgo"); r == nil {
		t.Error("case rename")
	}
	if err := s.MoveRequest("About.postgo", "About.postgo"); err != nil {
		t.Errorf("move to itself: %v", err)
	}
	if err := s.MoveRequest("health.postgo", "users/list.postgo"); !errors.Is(err, ErrExists) {
		t.Errorf("move over a request: %v", err)
	}
	if err := s.MoveRequest("health.postgo", "fixtures/a.postgo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("move into a non-folder: %v", err)
	}
	if err := s.MoveRequest("nope.postgo", "x.postgo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("move a missing request: %v", err)
	}
	if err := s.MoveRequest("nowhere/x.postgo", "x.postgo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("move from a missing folder: %v", err)
	}
	var ve *ValidationError
	if err := s.MoveRequest("health.postgo", "_bad.postgo"); !errors.As(err, &ve) {
		t.Errorf("move to a reserved name: %v", err)
	}
	write(t, dir, "taken.postgo", "GET x\n")
	if err := s.MoveRequest("health.postgo", "taken.postgo"); !errors.Is(err, ErrExists) {
		t.Errorf("move over a file the store does not know yet: %v", err)
	}

	ev.take()
	if err := s.DeleteRequest("health.postgo"); err != nil {
		t.Fatal(err)
	}
	if exists(dir, "health.postgo") || !strings.Contains(read(t, dir, "_collection.postgo"), "[order]\nusers\n") ||
		strings.Contains(read(t, dir, "_collection.postgo"), "health") {
		t.Errorf("after delete: %q", read(t, dir, "_collection.postgo"))
	}
	if evs := ev.take(); len(evs) != 2 || evs[0] != (Event{Kind: Deleted, Path: "health.postgo"}) {
		t.Errorf("events = %v", evs)
	}
	if err := s.DeleteRequest("health.postgo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
	if err := s.DeleteRequest("nowhere/x.postgo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete from a missing folder: %v", err)
	}
	// A broken file can be deleted.
	if err := s.DeleteRequest("broken.postgo"); err != nil || exists(dir, "broken.postgo") || len(s.Problems()) != 0 {
		t.Errorf("delete broken: %v %v", err, s.Problems())
	}
}

func TestFolders(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	ev := subscribe(s)

	if err := s.CreateFolder("orders"); err != nil {
		t.Fatal(err)
	}
	if !exists(dir, "orders/_folder.postgo") {
		t.Error("no _folder.postgo")
	}
	// It is a folder when read again, though empty.
	if f := open(t, dir).folder("orders"); f == nil {
		t.Error("an empty folder is lost on reading")
	}
	if err := s.CreateFolder("orders"); !errors.Is(err, ErrExists) {
		t.Errorf("create again: %v", err)
	}
	if err := s.CreateFolder("Orders"); !errors.Is(err, ErrExists) {
		t.Errorf("create in another case: %v", err)
	}
	if err := s.CreateFolder("missing/sub"); !errors.Is(err, ErrNotFound) {
		t.Errorf("create in a missing folder: %v", err)
	}
	var ve *ValidationError
	for _, bad := range []string{"_x", ".x", "environments", "a:b", ""} {
		if err := s.CreateFolder(bad); !errors.As(err, &ve) {
			t.Errorf("CreateFolder(%q) = %v", bad, err)
		}
	}
	// A directory of other files becomes a folder.
	if err := s.CreateFolder("fixtures"); err != nil {
		t.Errorf("CreateFolder over a directory without requests: %v", err)
	}
	write(t, dir, "taken/_folder.postgo", "")
	if err := s.CreateFolder("taken"); !errors.Is(err, ErrExists) {
		t.Errorf("CreateFolder over an unread _folder.postgo: %v", err)
	}

	ev.take()
	if err := s.MoveFolder("users", "orders/people"); err != nil {
		t.Fatal(err)
	}
	c := s.Collection()
	if r, _ := c.Find("orders/people/admin/ban.postgo"); r == nil {
		t.Errorf("tree after moving = %v", tree(c.Root))
	}
	if s.Version("orders/people/list.postgo") == "" || s.Version("users/list.postgo") != "" {
		t.Error("versions not moved")
	}
	evs := ev.take()
	var renamed []string
	for _, e := range evs {
		if e.Kind == Renamed {
			renamed = append(renamed, e.OldPath+">"+e.Path)
		}
	}
	if len(renamed) != 5 || renamed[0] != "users>orders/people" {
		t.Errorf("renames = %v", renamed)
	}
	if strings.Contains(read(t, dir, "_collection.postgo"), "users") {
		t.Error("the root's order kept the moved folder")
	}
	if err := c.Validate(); err.HasErrors() {
		t.Errorf("invalid after moving: %v", err)
	}
	// Its order moved with it.
	f := s.folder("orders/people")
	if nodeName(f.Children[0]) != "create.postgo" {
		t.Errorf("order of the moved folder = %v", tree(f))
	}

	for _, c := range []struct {
		from, to string
		err      error
	}{
		{"", "x", nil}, {"orders", "orders/people/x", nil}, {"nope", "x", ErrNotFound}, {"orders", "nowhere/x", ErrNotFound},
		{"orders/people", "fixtures", ErrExists}, {"orders", "_x", nil},
	} {
		err := s.MoveFolder(c.from, c.to)
		if err == nil || c.err != nil && !errors.Is(err, c.err) {
			t.Errorf("MoveFolder(%q, %q) = %v", c.from, c.to, err)
		}
	}
	write(t, dir, "elsewhere/x.txt", "")
	if err := s.MoveFolder("orders/people", "elsewhere"); !errors.Is(err, ErrExists) {
		t.Errorf("move over a directory: %v", err)
	}

	if err := s.DeleteFolder("orders"); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("delete a folder with things in it: %v", err)
	}
	if err := s.DeleteFolder("fixtures"); err != nil {
		t.Fatal(err)
	}
	if exists(dir, "fixtures/_folder.postgo") || !exists(dir, "fixtures/a.png") {
		t.Error("DeleteFolder took other files")
	}
	if err := s.DeleteFolder("fixtures"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
	if err := s.DeleteFolder(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete the root: %v", err)
	}
	s.CreateFolder("empty")
	if err := s.DeleteFolder("empty"); err != nil || exists(dir, "empty") {
		t.Errorf("an empty directory is left: %v", err)
	}
}

func TestSaveFolderAndOrder(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	users := s.folder("users")
	cfg := users.Config.Clone()
	cfg.Headers = []model.KV{{Key: "X-Team", Value: "core"}}
	v, err := s.SaveFolder("users", cfg, s.Version("users/_folder.postgo"))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "users/_folder.postgo"); got != "[headers]\nX-Team: core\n\n[vars]\nprefix = /v2\n\n[order]\ncreate.postgo\n" {
		t.Errorf("_folder.postgo = %q", got)
	}
	if v != s.Version("users/_folder.postgo") {
		t.Error("version")
	}
	if _, err := s.SaveFolder("users", cfg, "stale"); !errors.Is(err, ErrConflict) {
		t.Errorf("stale: %v", err)
	}
	// A folder without a file gets one, with base "".
	if _, err := s.SaveFolder("users/admin", model.Config{Auth: model.Auth{Type: model.AuthNone}}, ""); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "users/admin/_folder.postgo"); got != "[auth none]\n" {
		t.Errorf("new _folder.postgo = %q", got)
	}
	var ve *ValidationError
	if _, err := s.SaveFolder("users", model.Config{}, s.Version("users/_folder.postgo")); !errors.As(err, &ve) || ve.Issues[0].Field != "auth.type" {
		t.Errorf("invalid config: %v", err)
	}
	if _, err := s.SaveFolder("", cfg, ""); err == nil {
		t.Error("SaveFolder of the root")
	}
	if _, err := s.SaveFolder("nope", cfg, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing folder: %v", err)
	}

	if err := s.SetOrder("users", []string{"list.postgo", "admin"}); err != nil {
		t.Fatal(err)
	}
	if got := tree(s.folder("users")); !reflect.DeepEqual(got, []string{"users/list.postgo", "users/admin/", "users/admin/ban.postgo", "users/create.postgo"}) {
		t.Errorf("tree = %v", got)
	}
	if !strings.HasSuffix(read(t, dir, "users/_folder.postgo"), "[order]\nlist.postgo\nadmin\n") {
		t.Errorf("order = %q", read(t, dir, "users/_folder.postgo"))
	}
	if err := s.SetOrder("", []string{"about.postgo"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "_collection.postgo"); !strings.HasSuffix(got, "[order]\nabout.postgo\n") || !strings.Contains(got, "name = My API") {
		t.Errorf("root order = %q", got)
	}
	if got := s.Collection().Root.Children[0]; nodeName(got) != "about.postgo" {
		t.Errorf("first = %v", nodeName(got))
	}
	// A folder without a file gets one for its order.
	if err := s.SetOrder("users/admin", []string{"ban.postgo"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "users/admin/_folder.postgo"); got != "[auth none]\n\n[order]\nban.postgo\n" {
		t.Errorf("admin = %q", got)
	}
	if err := s.SetOrder("", nil); err != nil || strings.Contains(read(t, dir, "_collection.postgo"), "[order]") {
		t.Errorf("clearing the order: %v", err)
	}
	if err := s.SetOrder("users", []string{"x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown name: %v", err)
	}
	if err := s.SetOrder("users", []string{"admin", "admin"}); err == nil {
		t.Error("twice")
	}
	if err := s.SetOrder("nope", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing folder: %v", err)
	}
	write(t, dir, "users/_folder.postgo", "junk\n")
	if err := s.SetOrder("users", nil); err == nil {
		t.Error("SetOrder over a broken _folder.postgo")
	}
	write(t, dir, "_collection.postgo", "junk\n")
	if err := s.SetOrder("", nil); err == nil {
		t.Error("SetOrder over a broken root")
	}
}

func TestSaveCollectionInfo(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	cfg := model.Config{Auth: model.Auth{Type: model.AuthBearer, Bearer: model.BearerAuth{Token: "{{token}}"}}}
	if _, err := s.SaveCollectionInfo("Renamed", "prod", cfg, s.Version("_collection.postgo")); err != nil {
		t.Fatal(err)
	}
	got := read(t, dir, "_collection.postgo")
	want := "[postgo]\nformat = 1\nid = " + collID + "\nname = Renamed\ndefault-env = prod\n\n[auth bearer]\ntoken = {{token}}\n\n[order]\nusers\nhealth.postgo\n"
	if got != want {
		t.Errorf("_collection.postgo = %q", got)
	}
	c := s.Collection()
	if c.Name != "Renamed" || c.DefaultEnv != "prod" || c.Root.Config.Auth.Type != model.AuthBearer {
		t.Errorf("collection = %+v", c)
	}
	var ve *ValidationError
	if _, err := s.SaveCollectionInfo("", "", cfg, s.Version("_collection.postgo")); !errors.As(err, &ve) {
		t.Errorf("no name: %v", err)
	}
	if _, err := s.SaveCollectionInfo("x", "staging", cfg, s.Version("_collection.postgo")); !errors.As(err, &ve) {
		t.Errorf("missing environment: %v", err)
	}
	if _, err := s.SaveCollectionInfo("x", "", model.Config{Auth: model.Auth{Type: model.AuthInherit}}, s.Version("_collection.postgo")); !errors.As(err, &ve) {
		t.Errorf("root inherits: %v", err)
	}
	if _, err := s.SaveCollectionInfo("x", "", cfg, "stale"); !errors.Is(err, ErrConflict) {
		t.Errorf("stale: %v", err)
	}
}

func TestEnvironments(t *testing.T) {
	dir := sample(t)
	s := open(t, dir)
	dev := s.Collection().Env("dev")
	// Change a shared variable and the overridden one, and add one.
	dev.Vars[0].Value = "https://api.dev2"
	dev.Vars[1].Value = "me2"
	dev.Vars = append(dev.Vars, model.KV{Key: "new", Value: "n"})
	if _, err := s.SaveEnvironment(dev, s.Version("environments/dev.postgo")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "environments/dev.postgo"); got != "[meta]\nname = Dev\n\n[vars]\nbaseUrl = https://api.dev2\nuser = shared\nnew = n\n\n[secrets]\ntoken\n" {
		t.Errorf("shared = %q", got)
	}
	if got := read(t, dir, ".postgo.local/environments/dev.postgo"); got != "[vars]\nuser = me2\nextra = local\n" {
		t.Errorf("local = %q", got)
	}
	if got := s.Collection().Env("dev").Vars; got[1].Value != "me2" || got[3].Key != "extra" {
		t.Errorf("merged = %v", got)
	}

	// Dropping the local-only variable drops it from the overrides.
	dev = s.Collection().Env("dev")
	dev.Vars = dev.Vars[:3]
	if _, err := s.SaveEnvironment(dev, s.Version("environments/dev.postgo")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, ".postgo.local/environments/dev.postgo"); got != "[vars]\nuser = me2\n" {
		t.Errorf("local = %q", got)
	}

	if err := s.SaveLocalOverrides("prod", []model.KV{{Key: "baseUrl", Value: "http://localhost"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.Collection().Env("prod").Vars[0].Value; got != "http://localhost" {
		t.Errorf("prod with override = %q", got)
	}
	if err := s.SaveLocalOverrides("prod", nil); err != nil || exists(dir, ".postgo.local/environments/prod.postgo") {
		t.Errorf("clearing overrides: %v", err)
	}
	if err := s.SaveLocalOverrides("prod", nil); err != nil {
		t.Errorf("clearing again: %v", err)
	}
	if err := s.SaveLocalOverrides("nope", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing env: %v", err)
	}
	var ve *ValidationError
	if err := s.SaveLocalOverrides("prod", []model.KV{{Key: "bad name"}}); !errors.As(err, &ve) {
		t.Errorf("bad name: %v", err)
	}

	staging := model.NewEnvironment("staging", "Staging")
	if _, err := s.SaveEnvironment(staging, ""); err != nil {
		t.Fatal(err)
	}
	if envs := s.Collection().Envs; len(envs) != 3 || envs[1].ID != "prod" || envs[2].ID != "staging" {
		t.Errorf("envs = %v", envs)
	}
	if _, err := s.SaveEnvironment(staging, ""); !errors.Is(err, ErrExists) {
		t.Errorf("create again: %v", err)
	}
	if _, err := s.SaveEnvironment(model.NewEnvironment("_x", ""), ""); !errors.As(err, &ve) {
		t.Errorf("bad id: %v", err)
	}

	if err := s.DeleteEnvironment("dev"); err != nil {
		t.Fatal(err)
	}
	if exists(dir, "environments/dev.postgo") || exists(dir, ".postgo.local/environments/dev.postgo") || s.Collection().Env("dev") != nil {
		t.Error("dev not deleted")
	}
	if err := s.DeleteEnvironment("dev"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
}

// fakeWatcher calls the store back when told.
type fakeWatcher struct {
	changed func([]string)
	stopped bool
	err     error
}

func (w *fakeWatcher) Watch(dir string, changed func([]string)) (func(), error) {
	w.changed = changed
	return func() { w.stopped = true }, w.err
}

func TestWatcher(t *testing.T) {
	dir := sample(t)
	w := &fakeWatcher{}
	s, err := Open(dir, Options{Watcher: w})
	if err != nil {
		t.Fatal(err)
	}
	ev := subscribe(s)

	// The store's own writes come back from the watcher as nothing.
	r, _ := s.Collection().Find("health.postgo")
	r.Name = "Health"
	s.SaveRequest(r, s.Version("health.postgo"))
	ev.take()
	w.changed([]string{filepath.Join(dir, "health.postgo")})
	if got := ev.take(); len(got) != 0 {
		t.Errorf("own write came back as %v", got)
	}

	// Changes made by others.
	write(t, dir, "users/list.postgo", "GET {{baseUrl}}/users?all\n")
	write(t, dir, "users/new.postgo", "GET {{baseUrl}}/new\n")
	os.Remove(filepath.Join(dir, "about.postgo"))
	write(t, dir, "users/admin/ban.postgo", "[query]\nno request line\n")
	w.changed([]string{"users"})
	want := []Event{
		{Kind: Changed, Path: "users/admin/ban.postgo", External: true},
		{Kind: Changed, Path: "users/list.postgo", External: true},
		{Kind: Created, Path: "users/new.postgo", External: true},
		{Kind: Deleted, Path: "about.postgo", External: true},
		{Kind: Problem, Path: "users/admin/ban.postgo", External: true},
	}
	if got := ev.take(); !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%v\nwant\n%v", got, want)
	}
	if r, _ := s.Collection().Find("users/new.postgo"); r == nil {
		t.Error("new request not read")
	}

	// A broken root keeps the last good state.
	write(t, dir, "_collection.postgo", "junk\n")
	w.changed(nil)
	if got := ev.take(); len(got) != 1 || got[0].Kind != Problem || got[0].Path != "_collection.postgo" {
		t.Errorf("broken root = %v", got)
	}
	if s.Collection().Name != "My API" {
		t.Error("lost the collection")
	}

	s.Close()
	s.Close()
	if !w.stopped {
		t.Error("Close did not stop the watcher")
	}
	if _, err := Open(dir, Options{Watcher: &fakeWatcher{err: errors.New("no inotify")}}); err == nil {
		t.Error("Open with a failing watcher")
	}
}

func TestSubscribeCancel(t *testing.T) {
	s := open(t, sample(t))
	n := 0
	cancel := s.Subscribe(func(Event) { n++ })
	s.CreateFolder("a")
	cancel()
	s.CreateFolder("b")
	if n != 1 {
		t.Errorf("%d events after cancel", n)
	}
}

func TestTwoStores(t *testing.T) {
	// The GUI and a headless MCP server have the same collection open.
	dir := sample(t)
	gui, mcp := open(t, dir), open(t, dir)
	r, _ := gui.Collection().Find("health.postgo")
	base := gui.Version("health.postgo")
	r2 := r.Clone()
	r2.Name = "by the agent"
	if _, err := mcp.SaveRequest(r2, mcp.Version("health.postgo")); err != nil {
		t.Fatal(err)
	}
	r.Name = "by the user"
	if _, err := gui.SaveRequest(r, base); !errors.Is(err, ErrConflict) {
		t.Errorf("the user's write over the agent's: %v", err)
	}
}

func TestConcurrentWrites(t *testing.T) {
	s := open(t, sample(t))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := model.NewRequest(model.KindHTTP)
			r.ID, r.URL = filepath.ToSlash(filepath.Join("users", "c"+string(rune('a'+i))+".postgo")), "https://a"
			v, err := s.SaveRequest(r, "")
			if err != nil {
				t.Error(err)
				return
			}
			for range 5 {
				r.Name += "x"
				if v, err = s.SaveRequest(r, v); err != nil {
					t.Error(err)
					return
				}
				s.Collection()
			}
		}()
	}
	wg.Wait()
	if n := len(s.folder("users").Children); n != 11 {
		t.Errorf("%d children", n)
	}
}

func TestEventKindString(t *testing.T) {
	if Renamed.String() != "renamed" || Problem.String() != "problem" {
		t.Error("String")
	}
	if !strings.Contains((&ValidationError{model.Issues{{Field: "a", Severity: model.Error, Msg: "b"}}}).Error(), "a: b") {
		t.Error("ValidationError")
	}
}

func TestScanEdgeCases(t *testing.T) {
	dir := sample(t)
	write(t, dir, "environments/bad.postgo", "[vars]\nno separator\n")
	write(t, dir, "environments/_skip.postgo", "[vars]\na = 1\n")
	write(t, dir, "environments/readme.txt", "x")
	write(t, dir, "environments/sub/x.postgo", "[vars]\na = 1\n")
	write(t, dir, ".postgo.local/environments/prod.postgo", "[vars]\nbroken\n")
	write(t, dir, "users/_folder.postgo", "junk\n")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "users"), filepath.Join(dir, "loop")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "health.postgo"), filepath.Join(dir, "linked.postgo")); err != nil {
			t.Fatal(err)
		}
	}
	s := open(t, dir)
	c := s.Collection()
	var paths []string
	for _, p := range s.Problems() {
		paths = append(paths, p.Path)
	}
	slices.Sort(paths)
	want := []string{".postgo.local/environments/prod.postgo", "broken.postgo", "environments/bad.postgo", "users/_folder.postgo"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("problems = %v", paths)
	}
	if len(c.Envs) != 2 {
		t.Errorf("envs = %v", c.Envs)
	}
	if runtime.GOOS != "windows" {
		if s.folder("loop") != nil {
			t.Error("followed a link to a directory")
		}
		if r, _ := c.Find("linked.postgo"); r == nil {
			t.Error("did not read a link to a file")
		}
	}
	// The folder whose file is broken still holds its requests.
	if r, _ := c.Find("users/list.postgo"); r == nil {
		t.Error("lost the requests of a folder with a broken file")
	}
}
