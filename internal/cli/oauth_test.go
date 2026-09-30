package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// The terminal ends a refused login with AUTH_DENIED, so the browser must not ask to go back and finish it.
func TestOAuthDeniedCallbackSaysSoInTheBrowser(t *testing.T) {
	a := newTestApp(t)
	oauthServer(t, a, func(http.ResponseWriter, *http.Request) { t.Error("token exchange after a denial") })
	var page string
	a.openBrowser = func(target string) error {
		u, err := url.Parse(target)
		if err != nil {
			return err
		}
		q := u.Query()
		callback, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			return err
		}
		callback.RawQuery = mapValues("state", q.Get("state"), "error", "access_denied").Encode()
		resp, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		page = string(body)
		return err
	}
	err := a.login(context.Background())
	if e, ok := err.(*commandError); !ok || e.Code != "AUTH_DENIED" {
		t.Fatalf("login error = %v", err)
	}
	if !strings.Contains(page, "denied or cancelled") || strings.Contains(page, "finish login") {
		t.Fatalf("browser page = %q", page)
	}
}

// login may close the server and exit the moment it has the result, so the page has to be complete by then.
// The channel is unbuffered, so the handler blocks on the hand-over until the test reads it.
func TestLoginCallbackAnswersTheBrowserBeforeHandingOver(t *testing.T) {
	for query, want := range map[string]struct{ page, code string }{
		"state=s&code=c":                        {"Return to the terminal to finish login.", ""},
		"state=s&error=access_denied":           {"Login was denied or cancelled.", "AUTH_DENIED"},
		"state=s&error=temporarily_unavailable": {"Login failed.", "LOGIN_FAILED"},
	} {
		result := make(chan callbackResult)
		handler := loginCallback("127.0.0.1:1", "s", result)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			handler(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/callback?"+query, nil))
			close(done)
		}()
		cb := <-result
		if page := rec.Body.String(); !strings.HasPrefix(page, want.page) || rec.Header().Get("Content-Length") != fmt.Sprint(len(page)) {
			t.Fatalf("%s: page %q not complete at hand-over", query, page)
		}
		<-done
		var e *commandError
		if want.code == "" && cb.err != nil || want.code != "" && (!errors.As(cb.err, &e) || e.Code != want.code) {
			t.Fatalf("%s: result error %v", query, cb.err)
		}
	}
}

func TestLoginTimeoutDoesNotPointAtTheTimeoutFlag(t *testing.T) {
	a := newTestApp(t)
	oauthServer(t, a, func(http.ResponseWriter, *http.Request) { t.Error("token exchange without a callback") })
	a.openBrowser = func(string) error { return nil }
	defer func(wait time.Duration) { loginWait = wait }(loginWait)
	loginWait = 200 * time.Millisecond
	err := a.login(context.Background())
	var e *commandError
	if !errors.As(err, &e) || e.Code != "TIMEOUT" || strings.Contains(e.Message, "--timeout") || !strings.Contains(e.Message, "nodit auth login") {
		t.Fatalf("login error = %v", err)
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
