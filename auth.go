package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const redirectURI = "http://127.0.0.1:8888/callback"
const authorizeURL = "https://accounts.spotify.com/authorize"
const tokenURL = "https://accounts.spotify.com/api/token"

func openBrowser(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "open", address).Run()
	case "linux":
		return exec.CommandContext(ctx, "xdg-open", address).Run()
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", address).Run()
	default:
		return fmt.Errorf("automatic browser opening is unsupported on %s", runtime.GOOS)
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func authorizationURL(endpoint, clientID, redirect, state, verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {clientID}, "response_type": {"code"}, "redirect_uri": {redirect}, "scope": {"playlist-read-private"}, "state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}}
	return endpoint + "?" + q.Encode()
}

type authResult struct {
	code string
	err  error
}

func callbackHandler(state string, results chan<- authResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state || state == "" {
			http.Error(w, "Invalid authorization state.", http.StatusBadRequest)
			return
		}
		result := authResult{code: q.Get("code")}
		if q.Get("error") != "" {
			result.err = fmt.Errorf("Spotify authorization denied or failed")
		} else if result.code == "" {
			result.err = fmt.Errorf("Spotify callback did not include an authorization code")
		}
		select {
		case results <- result:
		default:
		}
		if result.err != nil {
			http.Error(w, result.err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Authorization received. You can close this tab and return to the terminal.")
	})
}

func authenticate(ctx context.Context, client *http.Client, clientID string, prompts io.Writer) (string, error) {
	state, err := randomToken()
	if err != nil {
		return "", err
	}
	verifier, err := randomToken()
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:8888")
	if err != nil {
		return "", fmt.Errorf("listen for Spotify callback on port 8888: %w", err)
	}
	results := make(chan authResult, 1)
	server := &http.Server{Handler: callbackHandler(state, results), ReadHeaderTimeout: requestTimeout}
	defer server.Close()
	failures := make(chan error, 1)
	go func() { failures <- server.Serve(listener) }()
	address := authorizationURL(authorizeURL, clientID, redirectURI, state, verifier)
	if _, err := fmt.Fprintf(prompts, "Opening Spotify in your browser. If it doesn't open, use this URL:\n%s\n", address); err != nil {
		return "", err
	}
	if err := openBrowser(ctx, address); err != nil {
		fmt.Fprintln(prompts, "Could not open the browser automatically. Open the URL above to continue.")
	}
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for Spotify authorization: %w", ctx.Err())
	case err := <-failures:
		return "", fmt.Errorf("Spotify callback server: %w", err)
	case result := <-results:
		if result.err != nil {
			return "", result.err
		}
		return exchangeCode(ctx, client, tokenURL, clientID, redirectURI, verifier, result.code)
	}
}

func exchangeCode(ctx context.Context, client *http.Client, endpoint, clientID, redirect, verifier, code string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "code": {code}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Spotify token exchange failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", spotifyHTTPError("token exchange", resp)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("invalid Spotify token response")
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("Spotify token response missing access token")
	}
	return token.AccessToken, nil
}
