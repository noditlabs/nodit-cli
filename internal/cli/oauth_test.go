package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func oauthServer(t *testing.T, a *app, tokenHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(metadata{a.env.Issuer, a.env.Issuer + "/oauth2/authorize", a.env.Issuer + "/oauth2/token"})
	})
	mux.HandleFunc("/oauth2/token", tokenHandler)
	s := httptest.NewTLSServer(mux)
	t.Cleanup(s.Close)
	a.env.Issuer = s.URL
	a.httpClient = s.Client()
	return s
}

func TestOAuthPKCEAndState(t *testing.T) {
	a := newTestApp(t)
	var authorize url.Values
	var calls atomic.Int32
	oauthServer(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f := r.PostForm
		if r.Method != "POST" || f.Get("grant_type") != "authorization_code" || f.Get("code") != "mock-code" || f.Get("client_id") != clientID || f.Get("resource") != a.env.Resource || f.Get("redirect_uri") != authorize.Get("redirect_uri") || f.Get("client_secret") != "" || r.Header.Get("Authorization") != "" {
			t.Error("invalid public-client token exchange")
		}
		verifier := f.Get("code_verifier")
		hash := sha256.Sum256([]byte(verifier))
		if len(verifier) < 43 || base64.RawURLEncoding.EncodeToString(hash[:]) != authorize.Get("code_challenge") {
			t.Error("invalid PKCE verifier")
		}
		fmt.Fprint(w, `{"access_token":"secret-access","refresh_token":"secret-refresh","token_type":"Bearer","expires_in":1800}`)
	})
	a.openBrowser = func(target string) error {
		u, err := url.Parse(target)
		if err != nil {
			return err
		}
		authorize = u.Query()
		if authorize.Get("client_id") != clientID || authorize.Get("resource") != a.env.Resource || authorize.Get("scope") != "nodit.all offline_access" || authorize.Get("code_challenge_method") != "S256" || authorize.Get("response_type") != "code" {
			t.Error("invalid authorization parameters")
		}
		callback, err := url.Parse(authorize.Get("redirect_uri"))
		if err != nil {
			return err
		}
		if callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Path != "/callback" {
			t.Error("callback is not ephemeral loopback")
		}
		for _, state := range []string{"wrong-state", authorize.Get("state")} {
			callback.RawQuery = mapValues("state", state, "code", "mock-code").Encode()
			resp, err := http.Get(callback.String())
			if err != nil {
				return err
			}
			resp.Body.Close()
			if state == "wrong-state" && resp.StatusCode != 400 {
				t.Error("invalid state accepted")
			}
			if state != "wrong-state" && resp.StatusCode != 200 {
				t.Error("valid callback rejected")
			}
		}
		return nil
	}
	before := time.Now()
	if err := a.login(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := a.loadSession()
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken != "secret-access" || s.RefreshToken != "secret-refresh" || s.ExpiresAt.Before(before.Add(1799*time.Second)) || s.ExpiresAt.After(time.Now().Add(1801*time.Second)) || calls.Load() != 1 {
		t.Fatal("session or expiry not stored correctly")
	}
	if strings.Contains(fmt.Sprint(a.stdout)+fmt.Sprint(a.stderr), "secret-") {
		t.Fatal("login leaked credentials")
	}
}

func TestConcurrentRefreshRotatesOnce(t *testing.T) {
	a := newTestApp(t)
	var calls atomic.Int32
	oauthServer(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "original-refresh" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("resource") != a.env.Resource || r.Form.Get("client_id") != clientID {
			t.Error("invalid refresh parameters")
		}
		fmt.Fprint(w, `{"access_token":"next-access","refresh_token":"next-refresh","token_type":"Bearer","expires_in":1800}`)
	})
	if err := a.saveSession(&session{"expired-access", "original-refresh", time.Now().Add(-time.Minute), a.env.Issuer, a.env.Resource}); err != nil {
		t.Fatal(err)
	}
	b := *a
	results := make(chan error, 2)
	for _, client := range []*app{a, &b} {
		go func() {
			token, err := client.accessToken(context.Background())
			if err == nil && token != "next-access" {
				err = fmt.Errorf("wrong token")
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	s, err := a.loadSession()
	if err != nil || s.RefreshToken != "next-refresh" || calls.Load() != 1 {
		t.Fatal("refresh rotation raced")
	}
}

func TestLogoutDoesNotResurrectRefreshedSession(t *testing.T) {
	a := newTestApp(t)
	started, finish := make(chan struct{}), make(chan struct{})
	oauthServer(t, a, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		fmt.Fprint(w, `{"access_token":"next","refresh_token":"rotated","token_type":"Bearer","expires_in":1800}`)
	})
	_ = a.saveSession(&session{"expired", "refresh", time.Now().Add(-time.Minute), a.env.Issuer, a.env.Resource})
	refreshed, loggedOut := make(chan error, 1), make(chan error, 1)
	go func() { _, err := a.accessToken(context.Background()); refreshed <- err }()
	<-started
	go func() {
		loggedOut <- a.config.locked(context.Background(), func() error { return a.deleteCredential(sessionKey) })
	}()
	close(finish)
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	if err := <-loggedOut; err != nil {
		t.Fatal(err)
	}
	if s, err := a.loadSession(); err != nil || s != nil {
		t.Fatal("logout resurrected session")
	}
}

func TestOAuthRejectsUntrustedMetadata(t *testing.T) {
	a := newTestApp(t)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(metadata{a.env.Issuer, "https://other.example/authorize", "https://other.example/token"})
	}))
	defer s.Close()
	a.env.Issuer = s.URL
	a.httpClient = s.Client()
	if _, err := a.discover(context.Background()); err == nil {
		t.Fatal("untrusted metadata accepted")
	}
	for _, endpoint := range []string{"http://auth.example/token", "https://auth.example.evil/token", "https://user@auth.example/token", "https://auth.example/token?redirect=evil"} {
		if sameOrigin("https://auth.example", endpoint) {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}

func TestAuthFailureRedactionAndNoRetry(t *testing.T) {
	a := newTestApp(t)
	var calls atomic.Int32
	oauthServer(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"secret-refresh"}`)
	})
	_, err := a.exchange(context.Background(), a.env.Issuer+"/oauth2/token", mapValues("refresh_token", "secret-refresh"))
	if err == nil || strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
		t.Fatal("error leaked credentials or retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.discover(ctx); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}
