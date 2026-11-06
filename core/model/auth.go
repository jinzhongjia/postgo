package model

// AuthType is the authentication of a request, folder or collection.
type AuthType string

const (
	AuthInherit AuthType = "inherit" // the enclosing folder's or collection's
	AuthNone    AuthType = "none"
	AuthBasic   AuthType = "basic"
	AuthBearer  AuthType = "bearer"
	AuthAPIKey  AuthType = "apikey"
	AuthDigest  AuthType = "digest"
	AuthOAuth1  AuthType = "oauth1"
	AuthOAuth2  AuthType = "oauth2"
	AuthAWSV4   AuthType = "awsv4"
	AuthNTLM    AuthType = "ntlm"
	AuthJWT     AuthType = "jwt"
)

// AuthTypes lists every AuthType.
var AuthTypes = []AuthType{AuthInherit, AuthNone, AuthBasic, AuthBearer, AuthAPIKey, AuthDigest, AuthOAuth1, AuthOAuth2, AuthAWSV4, AuthNTLM, AuthJWT}

// Auth is an authentication: Type chooses which of the other fields
// applies. The others keep what the user entered, so switching the type
// back restores it; only the chosen type's fields are saved.
//
// Every string field is a template: secrets belong in secret variables,
// as {{clientSecret}}, not in the field.
type Auth struct {
	Type   AuthType
	Basic  BasicAuth
	Bearer BearerAuth
	APIKey APIKeyAuth
	Digest DigestAuth
	OAuth1 OAuth1Auth
	OAuth2 OAuth2Auth
	AWSV4  AWSV4Auth
	NTLM   NTLMAuth
	JWT    JWTAuth
}

// Placement is where a credential goes.
type Placement string

const (
	InHeader Placement = "header"
	InQuery  Placement = "query"
	InBody   Placement = "body" // OAuth 1.0 only
)

type BasicAuth struct {
	Username string
	Password string
}

type BearerAuth struct {
	Token string
	// Prefix precedes the token in the Authorization header; "Bearer"
	// when empty.
	Prefix string
}

type APIKeyAuth struct {
	Key   string
	Value string
	In    Placement // header or query
}

// DigestAuth answers the server's challenge. The fields after Password
// override what the challenge gives, for servers that need them preset.
type DigestAuth struct {
	Username    string
	Password    string
	Realm       string
	Nonce       string
	Algorithm   string // MD5, MD5-sess, SHA-256, SHA-256-sess, SHA-512-256, SHA-512-256-sess
	QOP         string // auth, auth-int
	NonceCount  string
	ClientNonce string
	Opaque      string
}

// DigestAlgorithms lists the digest algorithms.
var DigestAlgorithms = []string{"MD5", "MD5-sess", "SHA-256", "SHA-256-sess", "SHA-512-256", "SHA-512-256-sess"}

type OAuth1Auth struct {
	ConsumerKey     string
	ConsumerSecret  string
	Token           string
	TokenSecret     string
	SignatureMethod string // one of OAuth1SignatureMethods
	// PrivateKey is the PEM key of the RSA methods.
	PrivateKey      string
	Realm           string
	Callback        string
	Verifier        string
	Nonce           string // generated when empty
	Timestamp       string // the current time when empty
	Version         string // "1.0" when empty
	In              Placement
	IncludeBodyHash bool
	AddEmptyParams  bool
}

// OAuth1SignatureMethods lists the OAuth 1.0 signature methods.
var OAuth1SignatureMethods = []string{"HMAC-SHA1", "HMAC-SHA256", "HMAC-SHA512", "RSA-SHA1", "RSA-SHA256", "RSA-SHA512", "PLAINTEXT"}

// OAuth2Grant is an OAuth 2.0 grant type.
type OAuth2Grant string

const (
	GrantAuthorizationCode OAuth2Grant = "authorization_code"
	GrantClientCredentials OAuth2Grant = "client_credentials"
	GrantPassword          OAuth2Grant = "password"
	GrantImplicit          OAuth2Grant = "implicit"
)

// OAuth2Grants lists the OAuth 2.0 grant types.
var OAuth2Grants = []OAuth2Grant{GrantAuthorizationCode, GrantClientCredentials, GrantPassword, GrantImplicit}

// OAuth2Auth gets and refreshes an OAuth 2.0 access token. The tokens
// themselves are never part of the request: package auth caches them.
type OAuth2Auth struct {
	Grant        OAuth2Grant
	AuthURL      string
	TokenURL     string
	RefreshURL   string // TokenURL when empty
	ClientID     string
	ClientSecret string
	Scope        string
	Audience     string
	Resource     string
	RedirectURI  string
	State        string
	Username     string // password grant
	Password     string // password grant
	// PKCE adds a code challenge to the authorization code grant.
	PKCE         bool
	PKCEMethod   string // S256 or plain; S256 when empty
	CodeVerifier string // generated when empty
	// ClientAuth sends the client's credentials as Basic auth ("header")
	// or in the token request's body ("body").
	ClientAuth string
	// TokenPrefix precedes the token in the header; "Bearer" when empty.
	TokenPrefix string
	In          Placement // header or query
	// HeaderName is the header the token goes in; Authorization when
	// empty.
	HeaderName string
	// QueryParam is the query parameter the token goes in when In is
	// InQuery; access_token when empty.
	QueryParam  string
	AutoRefresh bool
	// AuthParams and TokenParams are extra parameters of the
	// authorization and token requests.
	AuthParams  []KV
	TokenParams []KV
}

type AWSV4Auth struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
	Region       string
	Service      string
}

type NTLMAuth struct {
	Username    string
	Password    string
	Domain      string
	Workstation string
}

// JWTAuth signs a JWT for each request.
type JWTAuth struct {
	Algorithm string // one of JWTAlgorithms
	// Secret is the key of the HMAC algorithms, base64 when SecretBase64.
	Secret       string
	SecretBase64 bool
	// PrivateKey is the PEM key of the other algorithms.
	PrivateKey string
	// Payload and Header are JSON objects; Header adds to the header the
	// algorithm implies.
	Payload string
	Header  string
	In      Placement // header or query
	// Prefix precedes the token in the Authorization header; "Bearer"
	// when empty.
	Prefix string
	// QueryParam is the query parameter of InQuery; token when empty.
	QueryParam string
}

// JWTAlgorithms lists the JWT signing algorithms.
var JWTAlgorithms = []string{"HS256", "HS384", "HS512", "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}
