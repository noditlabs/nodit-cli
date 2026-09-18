package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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

const clientID = "nodit-cli"
const oauthScopes = "nodit.all offline_access"

type metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

func mapValues(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v
}

func (a *app) request(ctx context.Context, method, endpoint string, form url.Values, dest any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return failure("AUTH_REQUEST_FAILED", "Cannot create authentication request.")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")
	client := *a.httpClient
	client.Timeout = time.Duration(a.timeoutMS) * time.Millisecond
	// Never forward OAuth credentials through an HTTP redirect.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e := failure("AUTH_REQUEST_FAILED", "Authentication server request failed.")
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The response body can contain credentials or user-controlled text.
		e := failure("AUTH_REJECTED", "Authentication server rejected the request.")
		e.HTTPStatus = resp.StatusCode
		return e
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err = d.Decode(dest); err != nil {
		return failure("INVALID_AUTH_RESPONSE", "Authentication server returned an invalid response.")
	}
	return nil
}

func (a *app) discover(ctx context.Context) (metadata, error) {
	var m metadata
	err := a.request(ctx, http.MethodGet, a.env.Issuer+"/.well-known/oauth-authorization-server", nil, &m)
	if err != nil {
		return m, err
	}
	if m.Issuer != a.env.Issuer || !sameOrigin(a.env.Issuer, m.AuthorizationEndpoint) || !sameOrigin(a.env.Issuer, m.TokenEndpoint) {
		return m, failure("INVALID_AUTH_SERVER", "OAuth discovery does not match the configured issuer.")
	}
	return m, nil
}

func sameOrigin(issuer, endpoint string) bool {
	a, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	b, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return a.Scheme == "https" && b.Scheme == a.Scheme && a.Host == b.Host && b.User == nil && b.RawQuery == "" && b.Fragment == ""
}

func (a *app) exchange(ctx context.Context, endpoint string, form url.Values) (*session, error) {
	form.Set("client_id", clientID)
	form.Set("resource", a.env.Resource)
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := a.request(ctx, http.MethodPost, endpoint, form, &result); err != nil {
		return nil, err
	}
	if result.AccessToken == "" || !strings.EqualFold(result.TokenType, "Bearer") || result.ExpiresIn <= 0 || result.ExpiresIn > 365*24*60*60 {
		return nil, failure("INVALID_TOKEN_RESPONSE", "Authentication server returned an invalid token response.")
	}
	return &session{result.AccessToken, result.RefreshToken, a.now().Add(time.Duration(result.ExpiresIn) * time.Second), a.env.Issuer, a.env.Resource}, nil
}

func randomValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *app) login(ctx context.Context) error {
	if a.noInteractive {
		return failure("INTERACTION_REQUIRED", "Browser login requires interaction.")
	}
	if _, err := a.credentialPresent(sessionKey); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	m, err := a.discover(ctx)
	if err != nil {
		return err
	}
	state, err := randomValue()
	if err != nil {
		return err
	}
	verifier, err := randomValue()
	if err != nil {
		return err
	}
	challenge := sha256.Sum256([]byte(verifier))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return failure("CALLBACK_LISTEN_FAILED", "Cannot open the local login callback.")
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/callback"
	type callbackResult struct {
		code string
		err  error
	}
	result := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Host != listener.Addr().String() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid login state.", http.StatusBadRequest)
			return
		}
		var cb callbackResult
		if q.Get("error") != "" {
			cb.err = failure("AUTH_DENIED", "Login was denied or cancelled.")
		} else if len(q["code"]) != 1 || q.Get("code") == "" {
			http.Error(w, "Missing authorization code.", http.StatusBadRequest)
			return
		} else {
			cb.code = q.Get("code")
		}
		select {
		case result <- cb:
			fmt.Fprintln(w, "Return to the terminal to finish login.")
		default:
			http.Error(w, "Login callback already received.", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	u, _ := url.Parse(m.AuthorizationEndpoint)
	u.RawQuery = mapValues("response_type", "code", "client_id", clientID, "redirect_uri", redirect, "scope", oauthScopes, "resource", a.env.Resource, "state", state, "code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]), "code_challenge_method", "S256").Encode()
	fmt.Fprintln(a.stderr, "Opening the browser for Nodit login...")
	if err = a.openBrowser(u.String()); err != nil {
		return failure("BROWSER_OPEN_FAILED", "Cannot open the browser.")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case cb := <-result:
		if cb.err != nil {
			return cb.err
		}
		s, err := a.exchange(ctx, m.TokenEndpoint, mapValues("grant_type", "authorization_code", "code", cb.code, "redirect_uri", redirect, "code_verifier", verifier))
		if err != nil {
			return err
		}
		if err = a.config.locked(ctx, func() error { return a.saveSession(s) }); err != nil {
			return err
		}
		return a.success(map[string]any{"loggedIn": true, "expiresAt": s.ExpiresAt})
	}
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Run()
}
