package model

// Kind is the protocol of a request.
type Kind string

const (
	KindHTTP    Kind = "http"
	KindGraphQL Kind = "graphql"
	KindWS      Kind = "ws"
	KindSSE     Kind = "sse"
	KindGRPC    Kind = "grpc"
)

// Kinds lists every Kind, in the order the UI offers them.
var Kinds = []Kind{KindHTTP, KindGraphQL, KindWS, KindSSE, KindGRPC}

// Request is one saved request, of any protocol.
//
// The fields a protocol does not use stay empty; Validate reports those
// that are set where they do not apply.
type Request struct {
	// ID is the request's path relative to the collection's root, as
	// "users/create-user.postgo".
	ID   string
	Name string
	Tags []string

	Kind Kind
	// Method is the HTTP method of HTTP and GraphQL requests (GraphQL:
	// POST or GET), empty for the other kinds.
	Method string
	// URL is the template of the URL, without its query: the query is
	// Query, the single source of the parameters. For gRPC it is
	// "host/package.Service/Method".
	URL string

	Query   []KV // HTTP, GraphQL, WS, SSE
	Path    []KV // HTTP: values of :name segments of the URL
	Headers []KV // HTTP, GraphQL, WS, SSE
	Body    Body // HTTP, GraphQL

	Auth     Auth
	Vars     []KV
	Scripts  Scripts
	Asserts  []Assert
	Settings Settings

	Messages []Message // WS, gRPC: saved message templates
	Metadata []KV      // gRPC
	Proto    Proto     // gRPC

	Examples []Example // HTTP
	Docs     string    // Markdown
}

// NewRequest returns a request of the kind with every default set: GET
// for HTTP, POST for GraphQL, no body (a GraphQL body for GraphQL),
// inherited auth, server reflection for gRPC.
func NewRequest(kind Kind) *Request {
	r := &Request{Kind: kind, Body: Body{Type: BodyNone}, Auth: Auth{Type: AuthInherit}}
	switch kind {
	case KindHTTP:
		r.Method = "GET"
	case KindGraphQL:
		r.Method = "POST"
		r.Body.Type = BodyGraphQL
	case KindGRPC:
		r.Proto = Proto{Source: ProtoReflection}
	}
	return r
}

// Scripts are the JavaScript sources run around a request.
type Scripts struct {
	BeforeRequest string
	AfterResponse string
	Tests         string
}

// Empty reports whether no script is set.
func (s Scripts) Empty() bool {
	return s.BeforeRequest == "" && s.AfterResponse == "" && s.Tests == ""
}

// Message is a saved message of a WebSocket or gRPC request.
type Message struct {
	Name    string
	Content string
}

// ProtoSource is where a gRPC request finds its service definitions.
type ProtoSource string

const (
	ProtoReflection ProtoSource = "reflection"
	ProtoFile       ProtoSource = "file"
)

// Proto describes the service definitions of a gRPC request.
type Proto struct {
	Source ProtoSource
	// File is the .proto file, relative to the collection's root, when
	// Source is ProtoFile.
	File string
}

// Example is a sample response saved with an HTTP request.
type Example struct {
	Name   string
	Status int
	Body   string
}
