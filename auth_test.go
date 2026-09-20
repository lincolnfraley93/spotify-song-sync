package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAuthorizationURL(t *testing.T) {
	u, _ := url.Parse(authorizationURL(authorizeURL, "client", redirectURI, "state", "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"))
	q := u.Query()
	if q.Get("code_challenge") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "playlist-read-private" || q.Get("state") != "state" || q.Get("redirect_uri") != redirectURI {
		t.Fatal(q)
	}
}
func TestCallback(t *testing.T) {
	for _, tt := range []struct {
		query             string
		status            int
		delivered, failed bool
	}{
		{"state=wrong&code=secret", 400, false, false},
		{"code=secret", 400, false, false},
		{"state=expected&error=access_denied", 400, true, true},
		{"state=expected", 400, true, true},
		{"state=expected&code=secret", 200, true, false},
	} {
		t.Run(tt.query, func(t *testing.T) {
			results := make(chan authResult, 1)
			w := httptest.NewRecorder()
			callbackHandler("expected", results).ServeHTTP(w, httptest.NewRequest("GET", "/callback?"+tt.query, nil))
			if w.Code != tt.status {
				t.Fatal(w.Code)
			}
			select {
			case r := <-results:
				if !tt.delivered || (r.err != nil) != tt.failed {
					t.Fatalf("unexpected result: %+v", r)
				}
			default:
				if tt.delivered {
					t.Fatal("no result")
				}
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("code leaked")
			}
		})
	}
}
func TestExchangeCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error(r.Method)
		}
		r.ParseForm()
		for k, v := range map[string]string{"grant_type": "authorization_code", "client_id": "client", "redirect_uri": redirectURI, "code_verifier": "verifier", "code": "code"} {
			if r.Form.Get(k) != v {
				t.Errorf("%s: %s", k, r.Form.Get(k))
			}
		}
		if r.Form.Has("client_secret") {
			t.Error("unexpected secret")
		}
		w.Write([]byte(`{"access_token":"token"}`))
	}))
	defer server.Close()
	token, err := exchangeCode(context.Background(), server.Client(), server.URL, "client", redirectURI, "verifier", "code")
	if err != nil || token != "token" {
		t.Fatalf("%s %v", token, err)
	}
}
func TestExchangeFailures(t *testing.T) {
	for _, tt := range []struct {
		status     int
		body, want string
	}{
		{400, `{"error":"secret"}`, "HTTP 400"}, {429, `{}`, "Retry-After: 10"}, {200, `{}`, "missing access token"}, {200, `bad`, "invalid Spotify token response"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(tt.status)
			w.Write([]byte(tt.body))
		}))
		_, err := exchangeCode(context.Background(), server.Client(), server.URL, "client", redirectURI, "verifier", "code")
		server.Close()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%v; want %s", err, tt.want)
		}
	}
}
