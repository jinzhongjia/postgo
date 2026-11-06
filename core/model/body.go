package model

// BodyType is the kind of a request's body.
type BodyType string

const (
	BodyNone      BodyType = "none"
	BodyJSON      BodyType = "json"
	BodyText      BodyType = "text"
	BodyXML       BodyType = "xml"
	BodyHTML      BodyType = "html"
	BodyGraphQL   BodyType = "graphql"
	BodyForm      BodyType = "form" // application/x-www-form-urlencoded
	BodyMultipart BodyType = "multipart"
	BodyFile      BodyType = "file"
)

// BodyTypes lists every BodyType.
var BodyTypes = []BodyType{BodyNone, BodyJSON, BodyText, BodyXML, BodyHTML, BodyGraphQL, BodyForm, BodyMultipart, BodyFile}

// IsRaw reports whether the body is text kept in Body.Raw.
func (t BodyType) IsRaw() bool {
	switch t {
	case BodyJSON, BodyText, BodyXML, BodyHTML, BodyGraphQL:
		return true
	}
	return false
}

// ContentType is the Content-Type the body implies, "" for none, file
// and multipart (whose boundary the engine chooses).
func (t BodyType) ContentType() string {
	switch t {
	case BodyJSON:
		return "application/json"
	case BodyText:
		return "text/plain"
	case BodyXML:
		return "application/xml"
	case BodyHTML:
		return "text/html"
	case BodyGraphQL:
		return "application/json"
	case BodyForm:
		return "application/x-www-form-urlencoded"
	}
	return ""
}

// Body is a request's body. Only the fields of its Type are used.
type Body struct {
	Type BodyType
	// Raw is the text of a raw body; for GraphQL, the query.
	Raw string
	// Variables is the JSON of a GraphQL body's variables.
	Variables string
	// Form is the fields of a form body.
	Form []KV
	// Parts is the fields of a multipart body.
	Parts []Part
	// File is the file sent as a file body, relative to the collection's
	// root.
	File string
}

// Part is a field of a multipart body: text, or a file when File is true,
// in which case Value is the file's path relative to the collection's
// root.
type Part struct {
	KV
	File bool
}
