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
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

// newDiscoveryServer returns an httptest.Server that serves an OIDC discovery
// document pointing its endpoints back at itself.
func newDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
			"userinfo_endpoint":      srv.URL + "/userinfo",
		})
	})
	return srv
}

func mustQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	return u.Query()
}

func TestGenericProvider_IsProviderConfigured(t *testing.T) {
	tests := []struct {
		name     string
		cfg      OAuthClientConfig
		expected bool
	}{
		{
			name:     "empty",
			cfg:      OAuthClientConfig{},
			expected: false,
		},
		{
			name: "credentials but no endpoints",
			cfg: OAuthClientConfig{
				Generic: GenericOAuthProviderConfig{ClientID: "id", ClientSecret: "secret"},
			},
			expected: false,
		},
		{
			name: "issuer discovery",
			cfg: OAuthClientConfig{
				Generic: GenericOAuthProviderConfig{ClientID: "id", ClientSecret: "secret", Issuer: "https://dex.example.com"},
			},
			expected: true,
		},
		{
			name: "explicit discovery url",
			cfg: OAuthClientConfig{
				Generic: GenericOAuthProviderConfig{ClientID: "id", ClientSecret: "secret", DiscoveryURL: "https://dex.example.com/.well-known/openid-configuration"},
			},
			expected: true,
		},
		{
			name: "explicit endpoints",
			cfg: OAuthClientConfig{
				Generic: GenericOAuthProviderConfig{
					ClientID:         "id",
					ClientSecret:     "secret",
					AuthorizationURL: "https://idp.example.com/auth",
					TokenURL:         "https://idp.example.com/token",
				},
			},
			expected: true,
		},
		{
			name: "auth URL without token URL is not enough",
			cfg: OAuthClientConfig{
				Generic: GenericOAuthProviderConfig{ClientID: "id", ClientSecret: "secret", AuthorizationURL: "https://idp.example.com/auth"},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsProviderConfigured(hubclient.OAuthProviderGeneric); got != tc.expected {
				t.Errorf("IsProviderConfigured = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestGenericProvider_AuthURL_Discovery(t *testing.T) {
	srv := newDiscoveryServer(t)
	defer srv.Close()

	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: GenericOAuthProviderConfig{
				ClientID:     "scion-web",
				ClientSecret: "secret",
				Issuer:       srv.URL,
			},
		},
	})

	authURL, err := svc.GetAuthorizationURLForClient(context.Background(), OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "https://hub.example.com/auth/callback/generic", "state123")
	if err != nil {
		t.Fatalf("GetAuthorizationURLForClient: %v", err)
	}
	if !strings.HasPrefix(authURL, srv.URL+"/auth?") {
		t.Fatalf("auth URL %q does not start with discovered auth endpoint", authURL)
	}
	q := mustQuery(t, authURL)
	if q.Get("client_id") != "scion-web" {
		t.Errorf("client_id = %q, want scion-web", q.Get("client_id"))
	}
	if q.Get("state") != "state123" {
		t.Errorf("state = %q, want state123", q.Get("state"))
	}
	if q.Get("scope") == "" {
		t.Errorf("scope is empty, want default openid email profile")
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
}

func TestGenericProvider_AuthURL_ExplicitEndpointsAndScopes(t *testing.T) {
	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: GenericOAuthProviderConfig{
				ClientID:         "scion-web",
				ClientSecret:     "secret",
				AuthorizationURL: "https://idp.example.com/authorize",
				TokenURL:         "https://idp.example.com/oauth/token",
				Scopes:           []string{"openid", "email"},
				Prompt:           "consent",
				AccessType:       "offline",
			},
		},
	})

	authURL, err := svc.GetAuthorizationURLForClient(context.Background(), OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "https://hub.example.com/cb", "s")
	if err != nil {
		t.Fatalf("GetAuthorizationURLForClient: %v", err)
	}
	if !strings.HasPrefix(authURL, "https://idp.example.com/authorize?") {
		t.Fatalf("auth URL %q does not use explicit auth endpoint", authURL)
	}
	q := mustQuery(t, authURL)
	if got := q.Get("scope"); got != "openid email" {
		t.Errorf("scope = %q, want openid email", got)
	}
	if got := q.Get("prompt"); got != "consent" {
		t.Errorf("prompt = %q, want consent", got)
	}
	if got := q.Get("access_type"); got != "offline" {
		t.Errorf("access_type = %q, want offline", got)
	}
}

func TestGenericProvider_AuthURL_PKCE(t *testing.T) {
	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: GenericOAuthProviderConfig{
				ClientID:         "scion-web",
				ClientSecret:     "secret",
				AuthorizationURL: "https://idp.example.com/auth",
				TokenURL:         "https://idp.example.com/token",
				PKCE:             true,
			},
		},
	})

	verifier, err := GeneratePKCEVerifier()
	if err != nil {
		t.Fatalf("GeneratePKCEVerifier: %v", err)
	}

	authURL, err := svc.getGenericAuthURLWithConfig(context.Background(), svc.config.Web.Generic, "https://hub.example.com/cb", "state", verifier)
	if err != nil {
		t.Fatalf("getGenericAuthURLWithConfig: %v", err)
	}
	q := mustQuery(t, authURL)
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Errorf("code_challenge is empty")
	}
	if got, want := q.Get("code_challenge"), pkceS256Challenge(verifier); got != want {
		t.Errorf("code_challenge = %q, want %q", got, want)
	}
}

func TestGenericProvider_ExchangeCode_Userinfo(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
			"userinfo_endpoint":      srv.URL + "/userinfo",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"sub":     "user-123",
			"email":   "user@example.com",
			"name":    "Test User",
			"picture": "https://example.com/avatar.png",
		})
	})

	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: GenericOAuthProviderConfig{
				ClientID:     "scion-web",
				ClientSecret: "secret",
				DiscoveryURL: srv.URL + "/.well-known/openid-configuration",
			},
		},
	})

	info, err := svc.ExchangeCodeForClient(context.Background(), OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "auth-code", "https://hub.example.com/cb", "", "")
	if err != nil {
		t.Fatalf("ExchangeCodeForClient: %v", err)
	}
	if info.ID != "user-123" {
		t.Errorf("ID = %q, want user-123", info.ID)
	}
	if info.Email != "user@example.com" {
		t.Errorf("Email = %q, want user@example.com", info.Email)
	}
	if info.Provider != hubclient.OAuthProviderGeneric {
		t.Errorf("Provider = %q, want %q", info.Provider, hubclient.OAuthProviderGeneric)
	}
}

func TestGenericProvider_ExchangeCode_BasicAuth(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var gotAuth string
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-456", "token_type": "Bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": "u1", "email": "u@example.com", "name": "U"})
	})

	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: GenericOAuthProviderConfig{
				ClientID:         "cid",
				ClientSecret:     "csecret",
				AuthorizationURL: srv.URL + "/auth",
				TokenURL:         srv.URL + "/token",
				UserInfoURL:      srv.URL + "/userinfo",
				Authentication:   "basic",
			},
		},
	})

	_, err := svc.ExchangeCodeForClient(context.Background(), OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "code", "https://hub.example.com/cb", "", "")
	if err != nil {
		t.Fatalf("ExchangeCodeForClient: %v", err)
	}
	wantPrefix := "Basic " + base64.StdEncoding.EncodeToString([]byte("cid:csecret"))
	if gotAuth != wantPrefix {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantPrefix)
	}
}

func TestGenericProvider_IssuerValidation(t *testing.T) {
	svc := NewOAuthService(OAuthConfig{})
	cfg := GenericOAuthProviderConfig{
		ClientID:                "id",
		ClientSecret:            "s",
		AuthorizationURL:        "https://idp.example.com/auth",
		TokenURL:                "https://idp.example.com/token",
		Issuer:                  "https://idp.example.com",
		RequireIssuerValidation: true,
	}

	// Mismatched iss — should fail before hitting the network.
	_, err := svc.exchangeGenericCodeWithConfig(context.Background(), cfg, "code", "https://hub.example.com/cb", "", "https://evil.example.com")
	if err == nil || !strings.Contains(err.Error(), "issuer mismatch") {
		t.Errorf("expected issuer mismatch error, got %v", err)
	}

	// Missing iss with requireIssuerValidation should also fail.
	_, err = svc.exchangeGenericCodeWithConfig(context.Background(), cfg, "code", "https://hub.example.com/cb", "", "")
	if err == nil || !strings.Contains(err.Error(), "issuer parameter missing") {
		t.Errorf("expected issuer missing error, got %v", err)
	}
}

func TestGenericProvider_ClaimMapping(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "token_type": "Bearer"})
	})
	// Non-standard claim keys: "user_id"/"mail"/"display_name"/"avatar" instead
	// of the standard sub/email/name/picture.
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"user_id":      "u-999",
			"mail":         "weird@example.com",
			"display_name": "Weird Claims User",
			"avatar":       "https://example.com/a.png",
		})
	})

	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: GenericOAuthProviderConfig{
				ClientID:         "cid",
				ClientSecret:     "csecret",
				AuthorizationURL: srv.URL + "/auth",
				TokenURL:         srv.URL + "/token",
				UserInfoURL:      srv.URL + "/userinfo",
				ClaimMapping: ClaimMapping{
					ID:      "user_id",
					Email:   "mail",
					Name:    "display_name",
					Picture: "avatar",
				},
			},
		},
	})

	info, err := svc.ExchangeCodeForClient(context.Background(), OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "code", "https://hub.example.com/cb", "", "")
	if err != nil {
		t.Fatalf("ExchangeCodeForClient: %v", err)
	}
	if info.ID != "u-999" {
		t.Errorf("ID = %q, want u-999", info.ID)
	}
	if info.Email != "weird@example.com" {
		t.Errorf("Email = %q, want weird@example.com", info.Email)
	}
	if info.DisplayName != "Weird Claims User" {
		t.Errorf("DisplayName = %q, want Weird Claims User", info.DisplayName)
	}
	if info.AvatarURL != "https://example.com/a.png" {
		t.Errorf("AvatarURL = %q, want https://example.com/a.png", info.AvatarURL)
	}
}

func TestGenericProvider_ClaimMapping_DefaultsToStandardClaims(t *testing.T) {
	claims := map[string]any{
		"sub":     "s1",
		"email":   "e@example.com",
		"name":    "N",
		"picture": "https://p",
	}
	info := userInfoFromClaims(claims, ClaimMapping{})
	if info == nil {
		t.Fatal("userInfoFromClaims returned nil, want a populated OAuthUserInfo")
	}
	if info.ID != "s1" || info.Email != "e@example.com" || info.DisplayName != "N" || info.AvatarURL != "https://p" {
		t.Errorf("unexpected info: %+v", info)
	}
}

func TestGenericProvider_ClaimMapping_MissingMappedClaim(t *testing.T) {
	claims := map[string]any{"sub": "s1", "email": "e@example.com"}
	// Mapping points ID at a claim key that isn't present.
	info := userInfoFromClaims(claims, ClaimMapping{ID: "missing_key"})
	if info != nil {
		t.Errorf("expected nil when mapped ID claim is missing, got %+v", info)
	}
}

func TestGenericProvider_OverrideUserInfo_PropagatesToUserInfo(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "token_type": "Bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": "u1", "email": "u@example.com", "name": "U"})
	})

	for _, override := range []bool{false, true} {
		svc := NewOAuthService(OAuthConfig{
			Web: OAuthClientConfig{
				Generic: GenericOAuthProviderConfig{
					ClientID:         "cid",
					ClientSecret:     "csecret",
					AuthorizationURL: srv.URL + "/auth",
					TokenURL:         srv.URL + "/token",
					UserInfoURL:      srv.URL + "/userinfo",
					OverrideUserInfo: override,
				},
			},
		})
		info, err := svc.ExchangeCodeForClient(context.Background(), OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "code", "https://hub.example.com/cb", "", "")
		if err != nil {
			t.Fatalf("ExchangeCodeForClient: %v", err)
		}
		if info.OverrideUserInfo != override {
			t.Errorf("OverrideUserInfo = %v, want %v", info.OverrideUserInfo, override)
		}
	}
}

func TestGenericProvider_DiscoveryCached(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
		})
	})

	svc := NewOAuthService(OAuthConfig{})
	cfg := GenericOAuthProviderConfig{
		ClientID:     "id",
		ClientSecret: "s",
		DiscoveryURL: srv.URL + "/.well-known/openid-configuration",
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.resolveGenericEndpoints(context.Background(), cfg); err != nil {
			t.Fatalf("resolveGenericEndpoints: %v", err)
		}
	}
	if hits != 1 {
		t.Errorf("discovery fetched %d times, want 1 (should be cached)", hits)
	}
}

func newDeviceFlowServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                        srv.URL,
			"authorization_endpoint":        srv.URL + "/auth",
			"token_endpoint":                srv.URL + "/token",
			"device_authorization_endpoint": srv.URL + "/device/code",
		})
	})
	return srv
}

func TestGenericProvider_ResolveEndpoints_DeviceAuthorizationFromDiscovery(t *testing.T) {
	srv := newDeviceFlowServer(t)
	defer srv.Close()

	svc := NewOAuthService(OAuthConfig{})
	cfg := GenericOAuthProviderConfig{
		ClientID:     "id",
		ClientSecret: "s",
		DiscoveryURL: srv.URL + "/.well-known/openid-configuration",
	}
	ep, err := svc.resolveGenericEndpoints(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolveGenericEndpoints: %v", err)
	}
	if ep.DeviceAuthURL != srv.URL+"/device/code" {
		t.Errorf("DeviceAuthURL = %q, want %s/device/code (from discovery, like Dex/Ory Hydra advertise)", ep.DeviceAuthURL, srv.URL)
	}
}

func TestGenericProvider_RequestDeviceCode(t *testing.T) {
	var gotClientID, gotScope string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/device/code", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotClientID = r.FormValue("client_id")
		gotScope = r.FormValue("scope")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dc-42",
			"user_code":        "WXYZ-1234",
			"verification_uri": "https://idp.example.com/device",
			"expires_in":       600,
			"interval":         5,
		})
	})

	svc := NewOAuthService(OAuthConfig{})
	cfg := GenericOAuthProviderConfig{
		ClientID:               "cid",
		ClientSecret:           "csecret",
		AuthorizationURL:       srv.URL + "/auth",
		TokenURL:               srv.URL + "/token",
		DeviceAuthorizationURL: srv.URL + "/device/code",
	}

	resp, err := svc.requestGenericDeviceCode(context.Background(), cfg)
	if err != nil {
		t.Fatalf("requestGenericDeviceCode: %v", err)
	}
	if resp.DeviceCode != "dc-42" {
		t.Errorf("DeviceCode = %q, want dc-42", resp.DeviceCode)
	}
	if resp.UserCode != "WXYZ-1234" {
		t.Errorf("UserCode = %q, want WXYZ-1234", resp.UserCode)
	}
	if gotClientID != "cid" {
		t.Errorf("client_id sent = %q, want cid", gotClientID)
	}
	if gotScope == "" {
		t.Error("scope was not sent")
	}
}

func TestGenericProvider_RequestDeviceCode_NotAdvertised(t *testing.T) {
	svc := NewOAuthService(OAuthConfig{})
	cfg := GenericOAuthProviderConfig{
		ClientID:         "cid",
		ClientSecret:     "csecret",
		AuthorizationURL: "https://idp.example.com/auth",
		TokenURL:         "https://idp.example.com/token",
		// No DeviceAuthorizationURL, and explicit endpoints skip discovery.
	}
	_, err := svc.requestGenericDeviceCode(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "device flow is not configured") {
		t.Errorf("expected a clear 'device flow is not configured' error, got %v", err)
	}
}

func TestGenericProvider_PollDeviceToken_AuthorizationPending(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
	})

	svc := NewOAuthService(OAuthConfig{})
	cfg := GenericOAuthProviderConfig{
		ClientID:         "cid",
		ClientSecret:     "csecret",
		AuthorizationURL: srv.URL + "/auth",
		TokenURL:         srv.URL + "/token",
	}

	_, err := svc.pollGenericDeviceToken(context.Background(), cfg, "dc-1")
	var authErr *DeviceAuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *DeviceAuthError, got %v (%T)", err, err)
	}
	if authErr.Code != "authorization_pending" {
		t.Errorf("Code = %q, want authorization_pending", authErr.Code)
	}
}

func TestGenericProvider_PollDeviceToken_Success(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var gotAuth, gotGrantType, gotDeviceCode string
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotAuth = r.Header.Get("Authorization")
		gotGrantType = r.FormValue("grant_type")
		gotDeviceCode = r.FormValue("device_code")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-device-1", "token_type": "Bearer"})
	})

	svc := NewOAuthService(OAuthConfig{})
	cfg := GenericOAuthProviderConfig{
		ClientID:         "cid",
		ClientSecret:     "csecret",
		AuthorizationURL: srv.URL + "/auth",
		TokenURL:         srv.URL + "/token",
		Authentication:   "basic",
	}

	tr, err := svc.pollGenericDeviceToken(context.Background(), cfg, "dc-7")
	if err != nil {
		t.Fatalf("pollGenericDeviceToken: %v", err)
	}
	if tr.AccessToken != "at-device-1" {
		t.Errorf("AccessToken = %q, want at-device-1", tr.AccessToken)
	}
	if gotGrantType != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Errorf("grant_type = %q, want the RFC 8628 device_code grant", gotGrantType)
	}
	if gotDeviceCode != "dc-7" {
		t.Errorf("device_code = %q, want dc-7", gotDeviceCode)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("cid:csecret"))
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q (basic auth method)", gotAuth, wantAuth)
	}
}
