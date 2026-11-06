package storage

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)

// clock is a settable time for a DB.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func open(t *testing.T) (*DB, *clock) {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "postgo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c := &clock{t: t0}
	d.now = c.now
	return d, c
}

func TestOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postgo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddHistory(Entry{CollectionID: "c", Method: "GET", URL: "u"}, nil); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if es, _ := d.History(HistoryQuery{}); len(es) != 1 {
		t.Errorf("reopened database has %d entries", len(es))
	}
	var v int
	d.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != len(migrations) {
		t.Errorf("user_version = %d", v)
	}
}

func TestOpenNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postgo.db")
	d, _ := Open(path)
	d.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1))
	d.Close()
	if _, err := Open(path); err == nil {
		t.Error("opened a database of a newer schema")
	}
}

func TestOpenMemory(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.SetSetting("a", 1); err != nil {
		t.Fatal(err)
	}
	var n int
	if ok, err := d.GetSetting("a", &n); !ok || err != nil || n != 1 {
		t.Errorf("GetSetting = %v %v %d", ok, err, n)
	}
}

func TestOpenBadPath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "dir", "x.db")); err == nil {
		t.Error("opened a database in a missing directory")
	}
}

func TestHistory(t *testing.T) {
	d, clk := open(t)
	add := func(e Entry, r *StoredResponse) int64 {
		t.Helper()
		id, err := d.AddHistory(e, r)
		if err != nil {
			t.Fatal(err)
		}
		clk.add(time.Minute)
		return id
	}
	a := add(Entry{CollectionID: "c1", RequestID: "users/list.postgo", Source: SourceUser, Method: "GET", URL: "https://api/users", Status: 200, Duration: 120 * time.Millisecond, Size: 42},
		&StoredResponse{Request: []byte("GET /users"), Headers: []byte("h"), Body: []byte("[]"), Timing: []byte(`{"ttfb":1}`)})
	b := add(Entry{CollectionID: "c1", RequestID: "users/create.postgo", Source: SourceAgent, Client: "claude-code", Method: "POST", URL: "https://api/users_100%"}, nil)
	c := add(Entry{CollectionID: "c2", Method: "GET", URL: "https://other/x", Err: "dial tcp: refused"}, nil)

	all, err := d.History(HistoryQuery{})
	if err != nil || len(all) != 3 || all[0].ID != c || all[2].ID != a {
		t.Fatalf("History = %v %v", all, err)
	}
	e := all[2]
	if !e.Time.Equal(t0) || e.Duration != 120*time.Millisecond || e.Size != 42 || e.Status != 200 || e.Source != SourceUser {
		t.Errorf("entry = %+v", e)
	}
	ids := func(q HistoryQuery) []int64 {
		es, err := d.History(q)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	checks := []struct {
		q    HistoryQuery
		want []int64
	}{
		{HistoryQuery{CollectionID: "c1"}, []int64{b, a}},
		{HistoryQuery{CollectionID: "c1", RequestID: "users/list.postgo"}, []int64{a}},
		{HistoryQuery{Source: SourceAgent}, []int64{b}},
		{HistoryQuery{Text: "post"}, []int64{b}},
		{HistoryQuery{Text: "100%"}, []int64{b}},
		{HistoryQuery{Text: "users_"}, []int64{b}},
		{HistoryQuery{Text: "_"}, []int64{b}},
		{HistoryQuery{Since: t0.Add(time.Minute)}, []int64{c, b}},
		{HistoryQuery{Before: c}, []int64{b, a}},
		{HistoryQuery{Limit: 1}, []int64{c}},
	}
	for _, ch := range checks {
		if got := ids(ch.q); !reflect.DeepEqual(got, ch.want) {
			t.Errorf("%+v = %v, want %v", ch.q, got, ch.want)
		}
	}

	r, err := d.Response(a)
	if err != nil || string(r.Body) != "[]" || string(r.Request) != "GET /users" || r.Truncated || string(r.Timing) != `{"ttfb":1}` {
		t.Errorf("Response = %+v %v", r, err)
	}
	if _, err := d.Response(b); err != ErrNotFound {
		t.Errorf("Response without one = %v", err)
	}

	if err := d.RenameRequest("c1", "users/list.postgo", "people/list.postgo"); err != nil {
		t.Fatal(err)
	}
	if got := ids(HistoryQuery{RequestID: "people/list.postgo"}); !reflect.DeepEqual(got, []int64{a}) {
		t.Errorf("after rename = %v", got)
	}

	n, err := d.DeleteHistory(HistoryQuery{CollectionID: "c1", Limit: 1})
	if err != nil || n != 2 {
		t.Errorf("DeleteHistory = %d %v", n, err)
	}
	if _, err := d.Response(a); err != ErrNotFound {
		t.Error("the response outlived its entry")
	}
}

func TestTruncate(t *testing.T) {
	d, _ := open(t)
	d.MaxBody = 4
	id, _ := d.AddHistory(Entry{}, &StoredResponse{Body: []byte("123456")})
	r, _ := d.Response(id)
	if string(r.Body) != "1234" || !r.Truncated {
		t.Errorf("Response = %q %v", r.Body, r.Truncated)
	}
}

func TestPruneHistory(t *testing.T) {
	d, clk := open(t)
	for range 5 {
		d.AddHistory(Entry{}, nil)
		clk.add(time.Hour)
	}
	// Now is t0+5h: the first two are older than 3h30m.
	n, err := d.PruneHistory(0, 3*time.Hour+30*time.Minute)
	if err != nil || n != 2 {
		t.Errorf("by age = %d %v", n, err)
	}
	n, err = d.PruneHistory(1, 0)
	if err != nil || n != 2 {
		t.Errorf("by count = %d %v", n, err)
	}
	if es, _ := d.History(HistoryQuery{}); len(es) != 1 || es[0].ID != 5 {
		t.Errorf("left %v", es)
	}
	if n, _ := d.PruneHistory(0, 0); n != 0 {
		t.Error("PruneHistory(0, 0) deleted")
	}
}

func TestAudit(t *testing.T) {
	d, clk := open(t)
	d.AppendAudit(AuditEntry{CollectionID: "c1", Client: "claude-code", Tool: "send_request", Args: "users/list", Result: "ok"})
	clk.add(time.Minute)
	d.AppendAudit(AuditEntry{CollectionID: "c1", Client: "cursor", Tool: "update_request", Result: "denied"})
	clk.add(time.Minute)
	d.AppendAudit(AuditEntry{CollectionID: "c2", Client: "cursor", Tool: "send_request", Result: "error: x"})
	count := func(q AuditQuery) int {
		as, err := d.Audit(q)
		if err != nil {
			t.Fatal(err)
		}
		return len(as)
	}
	if count(AuditQuery{}) != 3 || count(AuditQuery{Client: "cursor"}) != 2 || count(AuditQuery{Tool: "send_request"}) != 2 ||
		count(AuditQuery{CollectionID: "c1"}) != 2 || count(AuditQuery{Since: t0.Add(time.Minute)}) != 2 ||
		count(AuditQuery{Before: 2}) != 1 || count(AuditQuery{Limit: 1}) != 1 {
		t.Error("Audit filters")
	}
	as, _ := d.Audit(AuditQuery{Limit: 1})
	if as[0].Tool != "send_request" || as[0].Client != "cursor" || !as[0].Time.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("newest = %+v", as[0])
	}
}

func TestSettingsAndState(t *testing.T) {
	d, _ := open(t)
	type perms struct {
		Execute string
		Hosts   []string
	}
	var p perms
	if ok, err := d.GetSetting("agent", &p); ok || err != nil {
		t.Errorf("missing setting = %v %v", ok, err)
	}
	want := perms{Execute: "ask", Hosts: []string{"api.example.com"}}
	d.SetSetting("agent", want)
	d.SetSetting("agent", want) // replaces
	if ok, err := d.GetSetting("agent", &p); !ok || err != nil || !reflect.DeepEqual(p, want) {
		t.Errorf("GetSetting = %v %v %+v", ok, err, p)
	}
	d.SetState("tabs", []string{"a", "b"})
	var tabs []string
	if ok, _ := d.GetState("tabs", &tabs); !ok || len(tabs) != 2 {
		t.Errorf("GetState = %v", tabs)
	}
	if err := d.SetState("bad", func() {}); err == nil {
		t.Error("SetState of a func succeeded")
	}
	d.db.Exec("INSERT INTO state (key, value) VALUES ('broken', '{')")
	if _, err := d.GetState("broken", &tabs); err == nil {
		t.Error("GetState of broken JSON succeeded")
	}
}

func u(s string) *url.URL {
	p, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return p
}

func names(cs []*http.Cookie) string {
	var b bytes.Buffer
	for i, c := range cs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(c.Name + "=" + c.Value)
	}
	return b.String()
}

func TestJarRFC6265(t *testing.T) {
	d, clk := open(t)
	j := d.Jar("c1")
	if d.Jar("c1") != j {
		t.Error("Jar is not cached")
	}
	set := func(from string, cs ...*http.Cookie) { j.SetCookies(u(from), cs) }

	set("https://www.example.com/a/b",
		&http.Cookie{Name: "host", Value: "1"},                                                       // host-only, path /a
		&http.Cookie{Name: "dom", Value: "2", Domain: ".Example.COM", Path: "/"},                     // domain cookie
		&http.Cookie{Name: "sec", Value: "3", Secure: true, Path: "/"},                               // secure
		&http.Cookie{Name: "deep", Value: "4", Path: "/a/b/c"},                                       // longer path
		&http.Cookie{Name: "other", Value: "x", Domain: "other.com"},                                 // another domain: rejected
		&http.Cookie{Name: "ps", Value: "x", Domain: "com"},                                          // public suffix: rejected
		&http.Cookie{Name: "", Value: "x"},                                                           // no name: rejected
		&http.Cookie{Name: "gone", Value: "x", Expires: t0.Add(-time.Hour)},                          // already expired
		&http.Cookie{Name: "age", Value: "5", MaxAge: 60, Path: "/"},                                 // expires in a minute
		&http.Cookie{Name: "__Host-h", Value: "6", Secure: true, Path: "/"},                          // valid __Host-
		&http.Cookie{Name: "__Host-bad", Value: "x", Secure: true, Domain: "example.com", Path: "/"}, // __Host- with a domain
		&http.Cookie{Name: "__Secure-s", Value: "7", Secure: true, Path: "/"},
	)
	set("http://www.example.com/", &http.Cookie{Name: "insecure", Value: "x", Secure: true},
		&http.Cookie{Name: "__Secure-x", Value: "x"})

	checks := []struct{ url, want string }{
		{"https://www.example.com/a/b/c/d", "deep=4 host=1 dom=2 sec=3 age=5 __Host-h=6 __Secure-s=7"},
		{"https://www.example.com/a", "host=1 dom=2 sec=3 age=5 __Host-h=6 __Secure-s=7"},
		{"https://www.example.com/abc", "dom=2 sec=3 age=5 __Host-h=6 __Secure-s=7"},
		{"http://www.example.com/a", "host=1 dom=2 age=5"},
		{"https://api.example.com/", "dom=2"},
		{"https://example.com/", "dom=2"},
		{"https://notexample.com/", ""},
		{"wss://www.example.com:443/", "dom=2 sec=3 age=5 __Host-h=6 __Secure-s=7"},
	}
	for _, c := range checks {
		if got := names(j.Cookies(u(c.url))); got != c.want {
			t.Errorf("Cookies(%s) = %q, want %q", c.url, got, c.want)
		}
	}

	// Replacing keeps the creation time, so the order stays.
	clk.add(time.Second)
	set("https://www.example.com/", &http.Cookie{Name: "dom", Value: "22", Domain: "example.com", Path: "/"})
	if got := names(j.Cookies(u("https://api.example.com/"))); got != "dom=22" {
		t.Errorf("replaced = %q", got)
	}

	// Max-Age passes.
	clk.add(2 * time.Minute)
	if got := names(j.Cookies(u("http://www.example.com/a"))); got != "host=1 dom=22" {
		t.Errorf("after Max-Age = %q", got)
	}

	// Deleting with Max-Age < 0, and with a past Expires.
	set("https://www.example.com/a/b", &http.Cookie{Name: "host", MaxAge: -1},
		&http.Cookie{Name: "deep", Path: "/a/b/c", Expires: t0})
	if got := names(j.Cookies(u("https://www.example.com/a/b/c"))); got != "dom=22 sec=3 __Host-h=6 __Secure-s=7" {
		t.Errorf("after deleting = %q", got)
	}

	// Another collection's jar is separate, and the cookies persist.
	if len(d.Jar("c2").Cookies(u("https://www.example.com/"))) != 0 {
		t.Error("jars share cookies")
	}
	d2 := &DB{db: d.db, now: clk.now, jars: map[string]*Jar{}}
	if got := names(d2.Jar("c1").Cookies(u("https://www.example.com/"))); got != "dom=22 sec=3 __Host-h=6 __Secure-s=7" {
		t.Errorf("reloaded = %q", got)
	}
}

func TestJarIPAndPorts(t *testing.T) {
	d, _ := open(t)
	j := d.Jar("c")
	j.SetCookies(u("http://127.0.0.1:8080/"), []*http.Cookie{
		{Name: "ip", Value: "1"},
		{Name: "ipdom", Value: "2", Domain: "127.0.0.1"},
		{Name: "bad", Value: "x", Domain: "0.0.1"},
	})
	if got := names(j.Cookies(u("http://127.0.0.1:9000/x"))); got != "ip=1 ipdom=2" {
		t.Errorf("IP cookies = %q", got)
	}
	j.SetCookies(u("http://localhost:3000/"), []*http.Cookie{{Name: "l", Value: "1"}})
	if got := names(j.Cookies(u("http://LOCALHOST/"))); got != "l=1" {
		t.Errorf("localhost = %q", got)
	}
	// A public suffix may set a host-only cookie for itself.
	j.SetCookies(u("https://github.io/"), []*http.Cookie{{Name: "p", Value: "1", Domain: "github.io"}})
	if got := names(j.Cookies(u("https://github.io/"))); got != "p=1" {
		t.Errorf("public suffix host = %q", got)
	}
	if got := names(j.Cookies(u("https://x.github.io/"))); got != "" {
		t.Errorf("public suffix leaked to %q", got)
	}
	if j.Cookies(&url.URL{}) != nil {
		t.Error("cookies for no host")
	}
	j.SetCookies(&url.URL{}, []*http.Cookie{{Name: "a", Value: "b"}})
}

func TestJarIDN(t *testing.T) {
	d, _ := open(t)
	j := d.Jar("c")
	j.SetCookies(u("https://bücher.example/"), []*http.Cookie{{Name: "a", Value: "1", Domain: "bücher.example"}})
	if got := names(j.Cookies(u("https://xn--bcher-kva.example/"))); got != "a=1" {
		t.Errorf("IDN = %q", got)
	}
}

func TestDefaultPathAndMatch(t *testing.T) {
	for in, want := range map[string]string{"": "/", "x": "/", "/": "/", "/a": "/", "/a/": "/a", "/a/b": "/a"} {
		if got := defaultPath(in); got != want {
			t.Errorf("defaultPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		req, cookie string
		want        bool
	}{{"/a", "/a", true}, {"/a/b", "/a", true}, {"/ab", "/a", false}, {"/a/b", "/a/", true}, {"/", "/a", false}} {
		if pathMatch(c.req, c.cookie) != c.want {
			t.Errorf("pathMatch(%q, %q) != %v", c.req, c.cookie, c.want)
		}
	}
}

func TestJarEditing(t *testing.T) {
	d, clk := open(t)
	j := d.Jar("c")
	if err := j.Put(StoredCookie{Name: "a", Value: "1", Domain: ".Example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Put(StoredCookie{Name: "b", Value: "2", Domain: "api.example.com", Path: "/v1", Expires: t0.Add(time.Hour), HostOnly: true}); err != nil {
		t.Fatal(err)
	}
	j.Put(StoredCookie{Name: "c", Value: "3", Domain: "other.org"})
	if err := j.Put(StoredCookie{Domain: "x"}); err == nil {
		t.Error("Put without a name")
	}
	if err := j.Put(StoredCookie{Name: "x"}); err == nil {
		t.Error("Put without a domain")
	}
	all, err := j.All()
	if err != nil || len(all) != 3 {
		t.Fatalf("All = %v %v", all, err)
	}
	if all[0].Domain != "api.example.com" || !all[0].Persistent || all[1].Domain != "example.com" || all[1].Path != "/" || all[1].Persistent || !all[1].Creation.Equal(t0) {
		t.Errorf("All = %+v", all)
	}
	if got := names(j.Cookies(u("https://api.example.com/v1/x"))); got != "b=2 a=1" {
		t.Errorf("edited cookies = %q", got)
	}
	clk.add(2 * time.Hour)
	if all, _ := j.All(); len(all) != 2 {
		t.Errorf("All kept an expired cookie: %v", all)
	}
	j.Delete(".EXAMPLE.com", "/", "a")
	j.Delete("missing", "/", "x")
	if all, _ := j.All(); len(all) != 1 || all[0].Name != "c" {
		t.Errorf("after Delete: %v", all)
	}
	j.Put(StoredCookie{Name: "d", Value: "4", Domain: "deep.api.example.com"})
	if err := j.Clear("example.com"); err != nil {
		t.Fatal(err)
	}
	if all, _ := j.All(); len(all) != 1 || all[0].Name != "c" {
		t.Errorf("after Clear(domain): %v", all)
	}
	j.Clear("")
	if all, _ := j.All(); len(all) != 0 {
		t.Errorf("after Clear: %v", all)
	}
}

func TestJarDatabaseErrors(t *testing.T) {
	d, _ := open(t)
	j := d.Jar("c")
	j.Put(StoredCookie{Name: "a", Value: "1", Domain: "example.com"})
	d.db.Exec("DROP TABLE cookies")
	if err := j.setCookies(u("https://example.com/"), []*http.Cookie{{Name: "b", Value: "2"}}); err == nil {
		t.Error("setCookies without a table succeeded")
	}
	if got := names(j.Cookies(u("https://example.com/"))); got != "a=1" {
		t.Errorf("a failed write changed the jar: %q", got)
	}
	if j.Put(StoredCookie{Name: "c", Domain: "example.com"}) == nil || j.Delete("example.com", "/", "a") == nil || j.Clear("") == nil {
		t.Error("writes without a table succeeded")
	}
	fresh := d.Jar("other")
	if _, err := fresh.All(); err == nil {
		t.Error("loading without a table succeeded")
	}
	if fresh.Cookies(u("https://example.com/")) != nil || fresh.Put(StoredCookie{Name: "a", Domain: "x.com"}) == nil ||
		fresh.Delete("x", "/", "a") == nil || fresh.Clear("") == nil || fresh.setCookies(u("https://x.com/"), nil) == nil {
		t.Error("a jar that failed to load works")
	}
}

func TestDatabaseErrors(t *testing.T) {
	d, _ := open(t)
	d.db.Exec("DROP TABLE history")
	d.db.Exec("DROP TABLE audit")
	if _, err := d.AddHistory(Entry{}, nil); err == nil {
		t.Error("AddHistory")
	}
	if _, err := d.History(HistoryQuery{}); err == nil {
		t.Error("History")
	}
	if _, err := d.DeleteHistory(HistoryQuery{}); err == nil {
		t.Error("DeleteHistory")
	}
	if _, err := d.PruneHistory(1, time.Hour); err == nil {
		t.Error("PruneHistory")
	}
	if _, err := d.PruneHistory(1, 0); err == nil {
		t.Error("PruneHistory by count")
	}
	if err := d.AppendAudit(AuditEntry{}); err == nil {
		t.Error("AppendAudit")
	}
	if _, err := d.Audit(AuditQuery{}); err == nil {
		t.Error("Audit")
	}
	d.db.Exec("DROP TABLE responses")
	if _, err := d.Response(1); err == nil || err == ErrNotFound {
		t.Error("Response")
	}
	d.Close()
	if err := d.tx(func(*sql.Tx) error { return nil }); err == nil {
		t.Error("tx on a closed database")
	}
	if _, err := d.GetSetting("a", new(int)); err == nil {
		t.Error("GetSetting on a closed database")
	}
	if err := d.RenameRequest("c", "a", "b"); err == nil {
		t.Error("RenameRequest on a closed database")
	}
}

func TestResponseWriteFails(t *testing.T) {
	d, _ := open(t)
	d.db.Exec("DROP TABLE responses")
	if _, err := d.AddHistory(Entry{}, &StoredResponse{}); err == nil {
		t.Error("AddHistory without a responses table")
	}
	if es, _ := d.History(HistoryQuery{}); len(es) != 0 {
		t.Error("a failed AddHistory left its entry")
	}
}

func TestConcurrentJar(t *testing.T) {
	d, _ := open(t)
	j := d.Jar("c")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 10 {
				j.SetCookies(u("https://example.com/"), []*http.Cookie{{Name: fmt.Sprint("n", i), Value: fmt.Sprint(k)}})
				j.Cookies(u("https://example.com/"))
			}
		}()
	}
	wg.Wait()
	if all, _ := j.All(); len(all) != 8 {
		t.Errorf("%d cookies", len(all))
	}
}
