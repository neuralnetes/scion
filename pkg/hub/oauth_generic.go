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

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

// Generic OAuth/OIDC provider.
//
// Unlike the hardcoded Google/GitHub providers, the generic provider is
// configurable for any standards-compliant OAuth2/OIDC issuer (e.g. the
// in-cluster Dex). Modeled on Better Auth's genericOAuth plugin: point it at
// an issuer and let OIDC discovery resolve the endpoints, OR set the
// authorization/token/userinfo endpoints explicitly when the provider has no
// `.well-known/openid-configuration`.
//
// Configure via SCION_SERVER_OAUTH_<CLIENT>_GENERIC_{CLIENTID,CLIENTSECRET}
// plus either GENERIC_ISSUER (discovery) or the explicit
// GENERIC_{AUTHURL,TOKENURL,USERINFOURL}.

// genericEndpoints holds the resolved OAuth2/OIDC endpoints for the generic provider.
type genericEndpoints struct {
	AuthURL     string
	TokenURL    string
	UserInfoURL string
}

// oidcDiscovery is the subset of the OIDC discovery document we consume.
type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

// resolveGenericEndpoints returns the provider's endpoints, preferring explicit
// overrides and falling back to OIDC discovery (via DiscoveryURL, or derived
// from Issuer). Discovered documents are cached per discovery URL.
func (s *OAuthService) resolveGenericEndpoints(ctx context.Context, cfg OAuthProviderConfig) (genericEndpoints, error) {
	ep := genericEndpoints{
		AuthURL:     cfg.AuthorizationURL,
		TokenURL:    cfg.TokenURL,
		UserInfoURL: cfg.UserInfoURL,
	}

	// If both the authorize and token endpoints are explicit, no discovery needed.
	if ep.AuthURL != "" && ep.TokenURL != "" {
		return ep, nil
	}

	// Resolve the discovery document URL: explicit DiscoveryURL wins, otherwise
	// derive the standard well-known path from the issuer.
	discoveryURL := cfg.DiscoveryURL
	if discoveryURL == "" && cfg.Issuer != "" {
		discoveryURL = strings.TrimSuffix(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	}
	if discoveryURL == "" {
		return genericEndpoints{}, fmt.Errorf("generic OAuth provider requires a discoveryUrl or issuer (for discovery), or explicit authorizationUrl+tokenUrl")
	}

	disc, err := s.discoverOIDC(ctx, discoveryURL)
	if err != nil {
		return genericEndpoints{}, err
	}
	// Explicit overrides win over discovered values when both are present.
	if ep.AuthURL == "" {
		ep.AuthURL = disc.AuthorizationEndpoint
	}
	if ep.TokenURL == "" {
		ep.TokenURL = disc.TokenEndpoint
	}
	if ep.UserInfoURL == "" {
		ep.UserInfoURL = disc.UserinfoEndpoint
	}
	return ep, nil
}

// discoverOIDC fetches (and caches) the OIDC discovery document from the given
// discovery URL (a full .well-known/openid-configuration URL).
func (s *OAuthService) discoverOIDC(ctx context.Context, discoveryURL string) (*oidcDiscovery, error) {
	if discoveryURL == "" {
		return nil, fmt.Errorf("OIDC discovery URL is not configured")
	}

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
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OIDC discovery failed: %s - %s", resp.Status, string(body))
	}

	var disc oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		return nil, fmt.Errorf("failed to decode OIDC discovery document: %w", err)
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" {
		return nil, fmt.Errorf("OIDC discovery document missing required endpoints")
	}

	s.oidcMu.Lock()
	s.oidcCache[discoveryURL] = &disc
	s.oidcMu.Unlock()

	return &disc, nil
}

// getGenericAuthURLWithConfig generates an authorization URL for the generic
// provider using the given config.
func (s *OAuthService) getGenericAuthURLWithConfig(cfg OAuthProviderConfig, callbackURL, state string) (string, error) {
	if cfg.ClientID == "" {
		return "", fmt.Errorf("generic OAuth is not configured")
	}
	ep, err := s.resolveGenericEndpoints(context.Background(), cfg)
	if err != nil {
		return "", err
	}

	scopes := cfg.Scopes
	if strings.TrimSpace(scopes) == "" {
		scopes = "openid email profile"
	}

	params := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {callbackURL},
		"response_type": {"code"},
		"scope":         {scopes},
		"state":         {state},
	}

	return ep.AuthURL + "?" + params.Encode(), nil
}

// exchangeGenericCodeWithConfig exchanges an authorization code for user info
// against the generic provider using the given config.
func (s *OAuthService) exchangeGenericCodeWithConfig(ctx context.Context, cfg OAuthProviderConfig, code, callbackURL string) (*OAuthUserInfo, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("generic OAuth is not configured")
	}
	ep, err := s.resolveGenericEndpoints(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Standard OAuth2 authorization-code exchange (same form-POST shape as Google).
	tokenResp, err := s.exchangeCodeForToken(ctx, ep.TokenURL, cfg.ClientID, cfg.ClientSecret, code, callbackURL)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange generic OAuth code: %w", err)
	}

	userInfo, err := s.getGenericUserInfo(ctx, ep.UserInfoURL, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get generic OAuth user info: %w", err)
	}
	return userInfo, nil
}

// genericUserInfo is the subset of the standard OIDC userinfo response we use.
type genericUserInfo struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// getGenericUserInfo retrieves user information from a standard OIDC userinfo endpoint.
func (s *OAuthService) getGenericUserInfo(ctx context.Context, userinfoURL, accessToken string) (*OAuthUserInfo, error) {
	if userinfoURL == "" {
		return nil, fmt.Errorf("generic OAuth provider has no userinfo endpoint (set issuer for discovery or userInfoURL explicitly)")
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
		return nil, fmt.Errorf("failed to get user info: %s - %s", resp.Status, string(body))
	}

	var ui genericUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
		return nil, fmt.Errorf("failed to decode user info: %w", err)
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
