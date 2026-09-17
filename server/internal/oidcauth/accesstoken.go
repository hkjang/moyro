package oidcauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// Access tokens for /mcp. The MCP authorization specification makes this
// server an OAuth 2.1 resource server: Keycloak signs in the person and mints
// an access token, and the only question here is whether that token is one
// the live Keycloak issued and is still good. Audience binding — whether it
// was issued *for this server* — is left to the caller, which knows the
// resource identifier and the administrator's allow-list.

var (
	// ErrAccessTokenInvalid wraps every signature, issuer, expiry, and
	// not-before failure from the library so callers can log the specific
	// cause while answering the client with one refusal.
	ErrAccessTokenInvalid = errors.New("oidc access token rejected")
	// ErrAccessTokenIsIDToken marks a token whose typ header says it is an ID
	// token. An ID token is proof of a sign-in, not an API credential.
	ErrAccessTokenIsIDToken = errors.New("oidc token is an ID token, not an access token")
	// ErrAccessTokenBound marks a token with a cnf claim: it is bound to a
	// key (DPoP, mTLS) this server cannot verify, so possession alone is not
	// enough to honour it.
	ErrAccessTokenBound = errors.New("oidc access token is bound to a proof-of-possession key")
	// ErrAccessTokenNoSubject marks a token that names nobody.
	ErrAccessTokenNoSubject = errors.New("oidc access token has no subject")
)

// accessTokenSigningAlgorithms are the asymmetric algorithms a Keycloak JWKS
// can vouch for. HS* and none are never accepted: with a shared secret the
// JWKS could not be what signed the token.
var accessTokenSigningAlgorithms = []string{
	gooidc.RS256, gooidc.RS384, gooidc.RS512,
	gooidc.ES256, gooidc.ES384, gooidc.ES512,
	gooidc.PS256, gooidc.PS384, gooidc.PS512,
	gooidc.EdDSA,
}

// AccessToken is the verified, non-secret view of a bearer JWT the live
// provider signed. Audience and AuthorizedParty are both reported because a
// real Keycloak 26 puts the client id in azp and only "account" in aud.
type AccessToken struct {
	Subject         string
	Audience        []string
	AuthorizedParty string
	Scopes          []string
	Expiry          time.Time
	Claims          map[string]any
	// Config is the public configuration of the snapshot that verified the
	// token; IssuerURL is the canonical issuer, UsernameClaim the fallback
	// identity claim.
	Config Config
}

// LooksLikeJWT is the cheap shape test that separates "not a key" from "not a
// token of any kind we accept": three non-empty dot-separated segments.
func LooksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// VerifyAccessToken checks signature (against the provider JWKS, asymmetric
// algorithms only), issuer, expiry, and not-before through the library, then
// refuses ID tokens, key-bound tokens, and subject-less tokens itself. It
// deliberately does not check the audience; see AccessToken.
func (m *Manager) VerifyAccessToken(ctx context.Context, raw string) (*AccessToken, error) {
	s, err := m.load()
	if err != nil {
		return nil, err
	}
	if s.accessVerifier == nil {
		return nil, ErrDisabled
	}
	if !LooksLikeJWT(raw) {
		return nil, fmt.Errorf("%w: bearer is not a JWT", ErrAccessTokenInvalid)
	}
	verified, err := s.accessVerifier.Verify(gooidc.ClientContext(ctx, s.client), raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAccessTokenInvalid, err)
	}
	claims := map[string]any{}
	if err := verified.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: claims: %v", ErrAccessTokenInvalid, err)
	}
	// Keycloak stamps typ into both the header and the payload. The payload
	// copy is what the library hands back; a token that says "ID" there is a
	// login artefact even though the same key signed it.
	if typ := stringClaim(claims, "typ"); strings.EqualFold(typ, "ID") {
		return nil, ErrAccessTokenIsIDToken
	}
	if _, bound := claims["cnf"]; bound {
		return nil, ErrAccessTokenBound
	}
	subject := strings.TrimSpace(verified.Subject)
	if subject == "" {
		return nil, ErrAccessTokenNoSubject
	}
	return &AccessToken{
		Subject:         subject,
		Audience:        append([]string(nil), verified.Audience...),
		AuthorizedParty: stringClaim(claims, "azp"),
		Scopes:          strings.Fields(stringClaim(claims, "scope")),
		Expiry:          verified.Expiry,
		Claims:          claims,
		Config:          publicConfig(s.config),
	}, nil
}
