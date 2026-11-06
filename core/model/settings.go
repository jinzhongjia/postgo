package model

import "time"

// HTTPVersion is the HTTP version a request asks for.
type HTTPVersion string

const (
	HTTPAuto HTTPVersion = "auto" // HTTP/2 when the server offers it over TLS
	HTTP11   HTTPVersion = "1.1"
	HTTP2    HTTPVersion = "2"
	HTTP3    HTTPVersion = "3"
)

// HTTPVersions lists every HTTPVersion.
var HTTPVersions = []HTTPVersion{HTTPAuto, HTTP11, HTTP2, HTTP3}

// Settings are a request's options. Unset ones are inherited from the
// enclosing folders and the collection, then from Defaults.
type Settings struct {
	Timeout         Opt[time.Duration]
	FollowRedirects Opt[bool]
	MaxRedirects    Opt[int]
	VerifyTLS       Opt[bool]
	HTTPVersion     Opt[HTTPVersion]
	EncodeURL       Opt[bool]
}

// Defaults are the settings nothing overrides.
var Defaults = Settings{
	Timeout:         Some(30 * time.Second),
	FollowRedirects: Some(true),
	MaxRedirects:    Some(10),
	VerifyTLS:       Some(true),
	HTTPVersion:     Some(HTTPAuto),
	EncodeURL:       Some(true),
}

// Override returns s with the settings child sets replacing its own.
func (s Settings) Override(child Settings) Settings {
	return Settings{
		Timeout:         s.Timeout.Override(child.Timeout),
		FollowRedirects: s.FollowRedirects.Override(child.FollowRedirects),
		MaxRedirects:    s.MaxRedirects.Override(child.MaxRedirects),
		VerifyTLS:       s.VerifyTLS.Override(child.VerifyTLS),
		HTTPVersion:     s.HTTPVersion.Override(child.HTTPVersion),
		EncodeURL:       s.EncodeURL.Override(child.EncodeURL),
	}
}
