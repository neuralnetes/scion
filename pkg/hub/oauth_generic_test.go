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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

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
				Generic: OAuthProviderConfig{ClientID: "id", ClientSecret: "secret"},
			},
			expected: false,
		},
		{
			name: "issuer discovery",
			cfg: OAuthClientConfig{
				Generic: OAuthProviderConfig{ClientID: "id", ClientSecret: "secret", Issuer: "https://dex.example.com"},
			},
			expected: true,
		},
		{
			name: "explicit discovery url",
			cfg: OAuthClientConfig{
				Generic: OAuthProviderConfig{ClientID: "id", ClientSecret: "secret", DiscoveryURL: "https://dex.example.com/.well-known/openid-configuration"},
			},
			expected: true,
		},
		{
			name: "explicit endpoints",
			cfg: OAuthClientConfig{
				Generic: OAuthProviderConfig{
					ClientID:         "id",
					ClientSecret:     "secret",
					AuthorizationURL: "https://idp.example.com/auth",
					TokenURL:         "https://idp.example.com/token",
				},
			},
			expected: true,
		},
		{
			name: "explicit authorize only is not enough",
			cfg: OAuthClientConfig{
				Generic: OAuthProviderConfig{ClientID: "id", ClientSecret: "secret", AuthorizationURL: "https://idp.example.com/auth"},
			},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsProviderConfigured(hubclient.OAuthProviderGeneric); got != tt.expected {
				t.Errorf("IsProviderConfigured(generic) = %v, want %v", got, tt.expected)
			}
		})
	}
}

// newDiscoveryServer returns an httptest server that serves an OIDC discovery
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

func TestGenericProvider_AuthURL_Discovery(t *testing.T) {
	srv := newDiscoveryServer(t)
	defer srv.Close()

	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: OAuthProviderConfig{
				ClientID:     "scion-web",
				ClientSecret: "secret",
				Issuer:       srv.URL, // discovery derived from issuer
			},
		},
	})

	authURL, err := svc.GetAuthorizationURLForClient(OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "https://hub.example.com/auth/callback/generic", "state123")
	if err != nil {
		t.Fatalf("GetAuthorizationURLForClient: %v", err)
	}
	if !strings.HasPrefix(authURL, srv.URL+"/auth?") {
		t.Fatalf("auth URL %q does not start with discovered authorization endpoint %q", authURL, srv.URL+"/auth")
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "scion-web" {
		t.Errorf("client_id = %q, want scion-web", q.Get("client_id"))
	}
	if q.Get("state") != "state123" {
		t.Errorf("state = %q, want state123", q.Get("state"))
	}
	if q.Get("scope") != "openid email profile" {
		t.Errorf("scope = %q, want default openid email profile", q.Get("scope"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
}

func TestGenericProvider_AuthURL_ExplicitEndpointsAndScopes(t *testing.T) {
	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: OAuthProviderConfig{
				ClientID:         "scion-web",
				ClientSecret:     "secret",
				AuthorizationURL: "https://idp.example.com/authorize",
				TokenURL:         "https://idp.example.com/oauth/token",
				Scopes:           "openid email",
			},
		},
	})

	authURL, err := svc.GetAuthorizationURLForClient(OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "https://hub.example.com/cb", "s")
	if err != nil {
		t.Fatalf("GetAuthorizationURLForClient: %v", err)
	}
	if !strings.HasPrefix(authURL, "https://idp.example.com/authorize?") {
		t.Fatalf("auth URL %q does not use explicit authorization endpoint", authURL)
	}
	if got := mustQuery(t, authURL).Get("scope"); got != "openid email" {
		t.Errorf("scope = %q, want custom openid email", got)
	}
}

func TestGenericProvider_ExchangeCode(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
			"userinfo_endpoint":      srv.URL + "/userinfo",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-123", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer at-123" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"sub":     "dex-user-uuid",
			"email":   "alex@neuralnetes.com",
			"name":    "Alex",
			"picture": "https://img/avatar.png",
		})
	})

	svc := NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{
			Generic: OAuthProviderConfig{ClientID: "scion-web", ClientSecret: "secret", DiscoveryURL: srv.URL + "/.well-known/openid-configuration"},
		},
	})

	info, err := svc.ExchangeCodeForClient(context.Background(), OAuthClientTypeWeb, hubclient.OAuthProviderGeneric, "auth-code", "https://hub.example.com/cb")
	if err != nil {
		t.Fatalf("ExchangeCodeForClient: %v", err)
	}
	if info.ID != "dex-user-uuid" {
		t.Errorf("ID = %q, want dex-user-uuid", info.ID)
	}
	if info.Email != "alex@neuralnetes.com" {
		t.Errorf("Email = %q", info.Email)
	}
	if info.Provider != hubclient.OAuthProviderGeneric {
		t.Errorf("Provider = %q, want generic", info.Provider)
	}
}

func TestGenericProvider_DiscoveryCached(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
			"userinfo_endpoint":      srv.URL + "/userinfo",
		})
	})

	svc := NewOAuthService(OAuthConfig{})
	cfg := OAuthProviderConfig{ClientID: "id", ClientSecret: "s", DiscoveryURL: srv.URL + "/.well-known/openid-configuration"}
	for i := 0; i < 3; i++ {
		if _, err := svc.resolveGenericEndpoints(context.Background(), cfg); err != nil {
			t.Fatalf("resolveGenericEndpoints: %v", err)
		}
	}
	if hits != 1 {
		t.Errorf("discovery fetched %d times, want 1 (cached)", hits)
	}
}

func mustQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Query()
}
