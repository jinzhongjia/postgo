package storage

import (
	"cmp"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// StoredCookie is a cookie of a jar, with what RFC 6265 keeps of it.
type StoredCookie struct {
	Domain string // without a leading dot
	Path   string
	Name   string
	Value  string
	// HostOnly cookies go to Domain alone, not to its subdomains: those
	// set without a Domain attribute.
	HostOnly bool
	Secure   bool
	HTTPOnly bool
	SameSite http.SameSite
	// Persistent cookies had Expires or Max-Age. The jar keeps session
	// cookies across restarts too, as API clients do, until cleared.
	Persistent bool
	Expires    time.Time // zero for session cookies
	Creation   time.Time
	LastAccess time.Time
}

type cookieKey struct{ domain, path, name string }

// Jar is the cookies of a collection: an http.CookieJar after RFC 6265,
// which keeps them in the database. Its methods are safe from any
// goroutine.
type Jar struct {
	db           *DB
	collectionID string

	mu      sync.Mutex
	loaded  bool
	err     error
	cookies map[cookieKey]*StoredCookie
}

// Jar returns the cookie jar of a collection.
func (d *DB) Jar(collectionID string) *Jar {
	d.mu.Lock()
	defer d.mu.Unlock()
	j := d.jars[collectionID]
	if j == nil {
		j = &Jar{db: d, collectionID: collectionID}
		d.jars[collectionID] = j
	}
	return j
}

// load reads the jar's cookies once. j.mu is held.
func (j *Jar) load() error {
	if j.loaded {
		return j.err
	}
	j.loaded = true
	j.cookies = map[cookieKey]*StoredCookie{}
	rows, err := j.db.db.Query(`SELECT domain, path, name, value, host_only, secure, http_only, same_site, persistent, expires, creation, last_access
		FROM cookies WHERE collection_id = ?`, j.collectionID)
	if err != nil {
		j.err = err
		return err
	}
	defer rows.Close()
	for rows.Next() {
		c := &StoredCookie{}
		var ss int
		var exp, cre, acc int64
		if err := rows.Scan(&c.Domain, &c.Path, &c.Name, &c.Value, &c.HostOnly, &c.Secure, &c.HTTPOnly, &ss, &c.Persistent, &exp, &cre, &acc); err != nil {
			j.err = err
			return err
		}
		c.SameSite = http.SameSite(ss)
		if exp != 0 {
			c.Expires = time.Unix(0, exp)
		}
		c.Creation, c.LastAccess = time.Unix(0, cre), time.Unix(0, acc)
		j.cookies[cookieKey{c.Domain, c.Path, c.Name}] = c
	}
	j.err = rows.Err()
	return j.err
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func (j *Jar) save(tx *sql.Tx, c *StoredCookie) error {
	_, err := tx.Exec(`INSERT INTO cookies (collection_id, domain, path, name, value, host_only, secure, http_only, same_site, persistent, expires, creation, last_access)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (collection_id, domain, path, name) DO UPDATE SET value = excluded.value, host_only = excluded.host_only,
			secure = excluded.secure, http_only = excluded.http_only, same_site = excluded.same_site, persistent = excluded.persistent,
			expires = excluded.expires, creation = excluded.creation, last_access = excluded.last_access`,
		j.collectionID, c.Domain, c.Path, c.Name, c.Value, c.HostOnly, c.Secure, c.HTTPOnly, int(c.SameSite), c.Persistent,
		unixNano(c.Expires), unixNano(c.Creation), unixNano(c.LastAccess))
	return err
}

func (j *Jar) remove(tx *sql.Tx, k cookieKey) error {
	_, err := tx.Exec("DELETE FROM cookies WHERE collection_id = ? AND domain = ? AND path = ? AND name = ?", j.collectionID, k.domain, k.path, k.name)
	return err
}

// canonicalHost returns the host of a URL's host, lowercased, without its
// port, in ASCII.
func canonicalHost(host string) (string, error) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if net.ParseIP(host) != nil {
		return host, nil
	}
	return idna.Lookup.ToASCII(strings.ToLower(host))
}

func isIP(host string) bool { return net.ParseIP(host) != nil }

// domainMatch reports whether host is domain or one of its subdomains.
func domainMatch(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain) && !isIP(host)
}

// pathMatch reports whether a request's path is in a cookie's path.
func pathMatch(reqPath, cookiePath string) bool {
	if reqPath == cookiePath {
		return true
	}
	if strings.HasPrefix(reqPath, cookiePath) {
		return strings.HasSuffix(cookiePath, "/") || reqPath[len(cookiePath)] == '/'
	}
	return false
}

// defaultPath is the path of a cookie set without one (RFC 6265 5.1.4).
func defaultPath(p string) string {
	if p == "" || p[0] != '/' {
		return "/"
	}
	i := strings.LastIndex(p, "/")
	if i == 0 {
		return "/"
	}
	return p[:i]
}

func secureScheme(u *url.URL) bool { return u.Scheme == "https" || u.Scheme == "wss" }

// SetCookies implements http.CookieJar. Cookies that RFC 6265 rejects,
// as those of another domain or of a public suffix, are ignored.
func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.setCookies(u, cookies)
}

func (j *Jar) setCookies(u *url.URL, cookies []*http.Cookie) error {
	host, err := canonicalHost(u.Host)
	if err != nil || host == "" {
		return err
	}
	now := j.db.now()
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.load(); err != nil {
		return err
	}
	type op struct {
		k cookieKey
		c *StoredCookie // nil to delete
	}
	var ops []op
	for i, hc := range cookies {
		// Cookies set together keep their order: each is a nanosecond
		// younger than the one before.
		c, del, ok := newCookie(hc, u, host, now.Add(time.Duration(i)))
		if !ok {
			continue
		}
		k := cookieKey{c.Domain, c.Path, c.Name}
		if del {
			ops = append(ops, op{k: k})
			continue
		}
		if old := j.cookies[k]; old != nil {
			c.Creation = old.Creation
		}
		ops = append(ops, op{k, c})
	}
	if len(ops) == 0 {
		return nil
	}
	err = j.db.tx(func(tx *sql.Tx) error {
		for _, o := range ops {
			var err error
			if o.c == nil {
				err = j.remove(tx, o.k)
			} else {
				err = j.save(tx, o.c)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The map changes once the database has.
	for _, o := range ops {
		if o.c == nil {
			delete(j.cookies, o.k)
		} else {
			j.cookies[o.k] = o.c
		}
	}
	return nil
}

// newCookie makes the stored cookie of a Set-Cookie, and reports whether
// it deletes the cookie instead, or is rejected.
func newCookie(hc *http.Cookie, u *url.URL, host string, now time.Time) (c *StoredCookie, del, ok bool) {
	if hc.Name == "" {
		return nil, false, false
	}
	c = &StoredCookie{Name: hc.Name, Value: hc.Value, Secure: hc.Secure, HTTPOnly: hc.HttpOnly, SameSite: hc.SameSite, Creation: now, LastAccess: now}

	domain := strings.ToLower(strings.TrimPrefix(hc.Domain, "."))
	switch {
	case domain == "":
		c.Domain, c.HostOnly = host, true
	case isIP(host):
		if domain != host {
			return nil, false, false
		}
		c.Domain, c.HostOnly = host, true
	default:
		if a, err := idna.Lookup.ToASCII(domain); err == nil {
			domain = a
		}
		if ps, _ := publicsuffix.PublicSuffix(domain); ps == domain {
			if host != domain {
				return nil, false, false
			}
			c.Domain, c.HostOnly = host, true
		} else if !domainMatch(host, domain) {
			return nil, false, false
		} else {
			c.Domain = domain
		}
	}

	c.Path = hc.Path
	if c.Path == "" || c.Path[0] != '/' {
		c.Path = defaultPath(u.Path)
	}

	// Secure cookies come from secure origins only, and so do the
	// prefixed ones (RFC 6265bis 5.6).
	if c.Secure && !secureScheme(u) {
		return nil, false, false
	}
	if strings.HasPrefix(c.Name, "__Secure-") && !(c.Secure && secureScheme(u)) {
		return nil, false, false
	}
	if strings.HasPrefix(c.Name, "__Host-") && !(c.Secure && secureScheme(u) && c.HostOnly && c.Path == "/") {
		return nil, false, false
	}

	switch {
	case hc.MaxAge < 0:
		return c, true, true
	case hc.MaxAge > 0:
		c.Persistent, c.Expires = true, now.Add(time.Duration(hc.MaxAge)*time.Second)
	case !hc.Expires.IsZero():
		if !hc.Expires.After(now) {
			return c, true, true
		}
		c.Persistent, c.Expires = true, hc.Expires
	}
	return c, false, true
}

func (c *StoredCookie) expired(now time.Time) bool {
	return !c.Expires.IsZero() && !c.Expires.After(now)
}

// Cookies implements http.CookieJar: the cookies to send to u, the
// longest paths first, then the oldest.
func (j *Jar) Cookies(u *url.URL) []*http.Cookie {
	host, err := canonicalHost(u.Host)
	if err != nil || host == "" {
		return nil
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	now := j.db.now()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.load() != nil {
		return nil
	}
	var sel []*StoredCookie
	var expired []cookieKey
	for k, c := range j.cookies {
		if c.expired(now) {
			expired = append(expired, k)
			continue
		}
		if c.Secure && !secureScheme(u) {
			continue
		}
		if c.HostOnly && host != c.Domain || !c.HostOnly && !domainMatch(host, c.Domain) {
			continue
		}
		if !pathMatch(path, c.Path) {
			continue
		}
		c.LastAccess = now
		sel = append(sel, c)
	}
	if len(expired) > 0 {
		j.db.tx(func(tx *sql.Tx) error {
			for _, k := range expired {
				delete(j.cookies, k)
				j.remove(tx, k)
			}
			return nil
		})
	}
	slices.SortFunc(sel, func(a, b *StoredCookie) int {
		if c := cmp.Compare(len(b.Path), len(a.Path)); c != 0 {
			return c
		}
		if c := a.Creation.Compare(b.Creation); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	out := make([]*http.Cookie, len(sel))
	for i, c := range sel {
		out[i] = &http.Cookie{Name: c.Name, Value: c.Value}
	}
	return out
}

// All returns every cookie of the jar that has not expired, by domain,
// path and name.
func (j *Jar) All() ([]StoredCookie, error) {
	now := j.db.now()
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.load(); err != nil {
		return nil, err
	}
	var out []StoredCookie
	for _, c := range j.cookies {
		if !c.expired(now) {
			out = append(out, *c)
		}
	}
	slices.SortFunc(out, func(a, b StoredCookie) int {
		return cmp.Or(cmp.Compare(a.Domain, b.Domain), cmp.Compare(a.Path, b.Path), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

// Put adds or replaces a cookie, as the user edits it. An empty Path is
// "/"; a zero Creation is now.
func (j *Jar) Put(c StoredCookie) error {
	if c.Name == "" {
		return errors.New("storage: a cookie needs a name")
	}
	d, err := canonicalHost(strings.TrimPrefix(c.Domain, "."))
	if err != nil || d == "" {
		return errors.New("storage: a cookie needs a domain")
	}
	c.Domain = d
	if c.Path == "" {
		c.Path = "/"
	}
	now := j.db.now()
	if c.Creation.IsZero() {
		c.Creation = now
	}
	if c.LastAccess.IsZero() {
		c.LastAccess = now
	}
	c.Persistent = !c.Expires.IsZero()
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.load(); err != nil {
		return err
	}
	if err := j.db.tx(func(tx *sql.Tx) error { return j.save(tx, &c) }); err != nil {
		return err
	}
	j.cookies[cookieKey{c.Domain, c.Path, c.Name}] = &c
	return nil
}

// Delete deletes a cookie; deleting a missing one is not an error.
func (j *Jar) Delete(domain, path, name string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.load(); err != nil {
		return err
	}
	k := cookieKey{strings.ToLower(strings.TrimPrefix(domain, ".")), path, name}
	if err := j.db.tx(func(tx *sql.Tx) error { return j.remove(tx, k) }); err != nil {
		return err
	}
	delete(j.cookies, k)
	return nil
}

// Clear deletes the cookies of a domain and its subdomains, or every
// cookie when domain is "".
func (j *Jar) Clear(domain string) error {
	domain = strings.ToLower(strings.TrimPrefix(domain, "."))
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.load(); err != nil {
		return err
	}
	var keys []cookieKey
	for k := range j.cookies {
		if domain == "" || k.domain == domain || strings.HasSuffix(k.domain, "."+domain) {
			keys = append(keys, k)
		}
	}
	err := j.db.tx(func(tx *sql.Tx) error {
		for _, k := range keys {
			if err := j.remove(tx, k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range keys {
		delete(j.cookies, k)
	}
	return nil
}
