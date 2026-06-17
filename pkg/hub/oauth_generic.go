// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hub

// Generic OAuth2/OIDC provider — modeled on Better Auth's genericOAuth plugin.
//
// Supports any standards-compliant issuer (e.g. in-cluster Dex) via OIDC
// discovery or explicit endpoint config. Feature parity with Better Auth:
//   - OIDC discovery with caching (DiscoveryURL or Issuer-derived)
//   - Custom discovery headers (e.g. Epic-Client-ID)
//   - id_token JWT decoding as primary userinfo source (falls back to userinfo endpoint)
//   - Issuer validation against the `iss` callback parameter (RFC 9207)
//   - PKCE (RFC 7636) code verifier generation and exchange
//   - token_endpoint_auth_method: "post" (default) or "basic"
//   - Configurable scopes, prompt, access_type, response_type
//   - Discovery document cached per URL for the process lifetime
//
// Configure via SCION_SERVER_OAUTH_WEB_GENERIC_{CLIENTID,CLIENTSECRET,ISSUER}
// or the explicit GENERIC_{AUTHURL,TOKENURL,USERINFOURL} env vars.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

// genericEndpoints holds the fully-resolved OAuth2/OIDC endpoints.
type genericEndpoints struct {
	AuthURL     string
	TokenURL    string
	UserInfoURL string
	// Issuer is populated from the discovery document when present.
	Issuer string
}

// oidcDiscovery is the subset of the OIDC discovery document we consume.
type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

// resolveGenericEndpoints returns the provider's endpoints, preferring explicit
// overrides and falling back to OIDC discovery. Discovered documents are cached.
func (s *OAuthService) resolveGenericEndpoints(ctx context.Context, cfg GenericOAuthProviderConfig) (genericEndpoints, error) {
	ep := genericEndpoints{
		AuthURL:     cfg.AuthorizationURL,
		TokenURL:    cfg.TokenURL,
		UserInfoURL: cfg.UserInfoURL,
	}

	// Fast path: both critical endpoints are explicit, no discovery needed.
	if ep.AuthURL != "" && ep.TokenURL != "" {
		return ep, nil
	}

	discoveryURL := cfg.DiscoveryURL
	if discoveryURL == "" && cfg.Issuer != "" {
		discoveryURL = strings.TrimSuffix(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	}
	if discoveryURL == "" {
		return genericEndpoints{}, fmt.Errorf("generic OAuth: set discoveryUrl, issuer, or explicit authorizationUrl+tokenUrl")
	}

	disc, err := s.discoverOIDC(ctx, discoveryURL, cfg.discoveryHeaders())
	if err != nil {
		return genericEndpoints{}, err
	}
	if ep.AuthURL == "" {
		ep.AuthURL = disc.AuthorizationEndpoint
	}
	if ep.TokenURL == "" {
		ep.TokenURL = disc.TokenEndpoint
	}
	if ep.UserInfoURL == "" {
		ep.UserInfoURL = disc.UserinfoEndpoint
	}
	ep.Issuer = disc.Issuer
	return ep, nil
}

// discoveryHeaders parses the "Key: Value" slice into a map.
func (c *GenericOAuthProviderConfig) discoveryHeaders() map[string]string {
	if len(c.DiscoveryHeaders) == 0 {
		return nil
	}
	h := make(map[string]string, len(c.DiscoveryHeaders))
	for _, kv := range c.DiscoveryHeaders {
		parts := strings.SplitN(kv, ":", 2)
		if len(parts) == 2 {
			h[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return h
}

// discoverOIDC fetches (and caches) the OIDC discovery document.
func (s *OAuthService) discoverOIDC(ctx context.Context, discoveryURL string, headers map[string]string) (*oidcDiscovery, error) {
	s.oidcMu.RLock()
	cached, ok := s.oidcCache[discoveryURL]
	s.oidcMu.RUnlock()
	if ok {
		return cached, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", discoveryURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OIDC discovery failed (%s): %s", resp.Status, body)
	}

	var disc oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		return nil, fmt.Errorf("failed to decode OIDC discovery document: %w", err)
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" {
		return nil, fmt.Errorf("OIDC discovery document missing authorization_endpoint or token_endpoint")
	}

	s.oidcMu.Lock()
	s.oidcCache[discoveryURL] = &disc
	s.oidcMu.Unlock()

	return &disc, nil
}

// getGenericAuthURLWithConfig generates an authorization URL for the generic provider.
// The optional codeVerifier is used for PKCE — pass the plain verifier, this
// method derives the S256 challenge.
func (s *OAuthService) getGenericAuthURLWithConfig(ctx context.Context, cfg GenericOAuthProviderConfig, callbackURL, state, codeVerifier string) (string, error) {
	if cfg.ClientID == "" {
		return "", fmt.Errorf("generic OAuth is not configured")
	}
	ep, err := s.resolveGenericEndpoints(ctx, cfg)
	if err != nil {
		return "", err
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}
	responseType := cfg.ResponseType
	if responseType == "" {
		responseType = "code"
	}

	params := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {callbackURL},
		"response_type": {responseType},
		"scope":         {strings.Join(scopes, " ")},
		"state":         {state},
	}
	if cfg.Prompt != "" {
		params.Set("prompt", cfg.Prompt)
	}
	if cfg.AccessType != "" {
		params.Set("access_type", cfg.AccessType)
	}
	if cfg.PKCE && codeVerifier != "" {
		challenge := pkceS256Challenge(codeVerifier)
		params.Set("code_challenge", challenge)
		params.Set("code_challenge_method", "S256")
	}

	return ep.AuthURL + "?" + params.Encode(), nil
}

// exchangeGenericCodeWithConfig exchanges an authorization code for user info.
// Pass the PKCE verifier (plain, not hashed) when cfg.PKCE is true.
// Pass the callback `iss` parameter for issuer validation when cfg.RequireIssuerValidation is true.
func (s *OAuthService) exchangeGenericCodeWithConfig(ctx context.Context, cfg GenericOAuthProviderConfig, code, callbackURL, codeVerifier, callbackIss string) (*OAuthUserInfo, error) {
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("generic OAuth is not configured")
	}

	ep, err := s.resolveGenericEndpoints(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Issuer validation (RFC 9207) — mirrors Better Auth's requireIssuerValidation.
	expectedIssuer := cfg.Issuer
	if expectedIssuer == "" {
		expectedIssuer = ep.Issuer
	}
	if expectedIssuer != "" && callbackIss != "" && callbackIss != expectedIssuer {
		return nil, fmt.Errorf("OAuth issuer mismatch: expected %q, got %q", expectedIssuer, callbackIss)
	}
	if expectedIssuer != "" && callbackIss == "" && cfg.RequireIssuerValidation {
		return nil, fmt.Errorf("OAuth issuer parameter missing from callback (requireIssuerValidation is set)")
	}

	tokenResp, err := s.exchangeGenericCodeForToken(ctx, cfg, ep.TokenURL, code, callbackURL, codeVerifier)
	if err != nil {
		return nil, fmt.Errorf("generic OAuth token exchange failed: %w", err)
	}

	// Prefer id_token JWT decoding (no extra round-trip) over the userinfo endpoint,
	// exactly as Better Auth does. Fall back to userinfo when no id_token is present.
	if tokenResp.IDToken != "" {
		if info := userInfoFromIDToken(tokenResp.IDToken); info != nil {
			return info, nil
		}
	}

	return s.getGenericUserInfo(ctx, ep.UserInfoURL, tokenResp.AccessToken)
}

// exchangeGenericCodeForToken performs the token endpoint request, honoring the
// configured authentication method ("post" body params or "basic" HTTP auth).
func (s *OAuthService) exchangeGenericCodeForToken(ctx context.Context, cfg GenericOAuthProviderConfig, tokenURL, code, callbackURL, codeVerifier string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {callbackURL},
	}
	if cfg.PKCE && codeVerifier != "" {
		data.Set("code_verifier", codeVerifier)
	}

	authMethod := cfg.Authentication
	if authMethod == "" {
		authMethod = "post"
	}

	// For "post", embed credentials in the body. Build the body first.
	if authMethod != "basic" {
		data.Set("client_id", cfg.ClientID)
		data.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if authMethod == "basic" {
		req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed (%s): %s", resp.Status, body)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}
	return &tr, nil
}

// idTokenClaims is the subset of JWT claims we extract from an id_token.
type idTokenClaims struct {
	jwt.Claims
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// userInfoFromIDToken attempts to decode an OIDC id_token JWT without signature
// verification (the token already arrived via TLS from the token endpoint) and
// extract user info from standard claims. Returns nil if the token is malformed
// or missing required claims.
func userInfoFromIDToken(rawIDToken string) *OAuthUserInfo {
	// go-jose v4 requires specifying accepted algorithms. We accept the common
	// OIDC signing algorithms; signature is not verified since the token came
	// directly from the token endpoint over TLS.
	tok, err := jwt.ParseSigned(rawIDToken, []jose.SignatureAlgorithm{
		jose.RS256, jose.RS384, jose.RS512,
		jose.ES256, jose.ES384, jose.ES512,
		jose.PS256, jose.PS384, jose.PS512,
		jose.EdDSA,
	})
	if err != nil {
		return nil
	}
	var claims idTokenClaims
	if err := tok.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return nil
	}
	if claims.Subject == "" || claims.Email == "" {
		return nil
	}
	return &OAuthUserInfo{
		ID:          claims.Subject,
		Email:       claims.Email,
		DisplayName: claims.Name,
		AvatarURL:   claims.Picture,
		Provider:    hubclient.OAuthProviderGeneric,
	}
}

// genericUserInfoResponse is the standard OIDC userinfo response shape.
type genericUserInfoResponse struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// getGenericUserInfo retrieves user information from the OIDC userinfo endpoint.
func (s *OAuthService) getGenericUserInfo(ctx context.Context, userinfoURL, accessToken string) (*OAuthUserInfo, error) {
	if userinfoURL == "" {
		return nil, fmt.Errorf("generic OAuth has no userinfo endpoint — set issuer for discovery or userInfoUrl explicitly")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", userinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("userinfo request failed (%s): %s", resp.Status, body)
	}

	var ui genericUserInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo response: %w", err)
	}
	if ui.Sub == "" || ui.Email == "" {
		return nil, fmt.Errorf("generic OAuth userinfo missing required sub/email claims")
	}

	return &OAuthUserInfo{
		ID:          ui.Sub,
		Email:       ui.Email,
		DisplayName: ui.Name,
		AvatarURL:   ui.Picture,
		Provider:    hubclient.OAuthProviderGeneric,
	}, nil
}

// GeneratePKCEVerifier generates a cryptographically random PKCE code verifier
// (RFC 7636 §4.1). Call this before GetAuthorizationURLForClient and store the
// verifier in the session to pass to ExchangeCodeForClient on callback.
func GeneratePKCEVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceS256Challenge derives the S256 code challenge from a plain verifier.
func pkceS256Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
