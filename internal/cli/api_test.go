package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAddress = "0x000000000000000000000000000000000000dEaD"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockAPI(a *app, handler roundTripFunc) { a.httpClient = &http.Client{Transport: handler} }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestDataRequestAndCredentialPrecedence(t *testing.T) {
	for _, suffix := range []string{"alpha", "beta", "release"} {
		t.Run(suffix, func(t *testing.T) {
			a := newTestApp(t)
			a.env = testEnvironment(t, suffix)
			projectID, keyID := "project-1", "key-1"
			_ = a.keys.Set(projectCredentialKey(projectID, keyID), "stored-key")
			if err := a.config.update(context.Background(), func(c *config) error {
				c.Project = projectID
				c.ProjectKeys = map[string]string{projectID: keyID}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			envKey := "environment-key"
			a.getenv = func(name string) string {
				switch name {
				case "NODIT_API_KEY":
					return envKey
				case "NODIT_AUTH_TOKEN":
					return "unused-oauth-token"
				}
				return ""
			}
			wantKey := "explicit-key"
			mockAPI(a, func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != "web3."+a.env.Domain || r.URL.Path != "/v1/ethereum/mainnet/native/getNativeBalanceByAccount" {
					t.Fatalf("wrong endpoint: %s", r.URL)
				}
				if r.Header.Get("X-API-KEY") != wantKey || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
					t.Fatal("wrong credentials")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["accountAddress"] != testAddress || len(body) != 1 {
					t.Fatalf("wrong body: %v %v", body, err)
				}
				return response(200, `{"ownerAddress":"`+testAddress+`","balance":"123456789012345678901234567890"}`), nil
			})
			args := []string{"data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet", "-o", "json"}
			for _, source := range []string{"explicit", "env", "stored"} {
				runArgs := append([]string{}, args...)
				if source == "explicit" {
					runArgs = append(runArgs, "--api-key", wantKey)
				}
				if source == "env" {
					wantKey = envKey
				}
				if source == "stored" {
					envKey = ""
					wantKey = "stored-key"
				}
				c, out, e := run(t, a, runArgs...)
				if c != 0 || e != "" || !strings.Contains(out, `"balance": "123456789012345678901234567890"`) {
					t.Fatalf("%d %s %s", c, out, e)
				}
			}
		})
	}
}

func TestDataNetworkPrecedenceAndValidationBeforeRequest(t *testing.T) {
	a := newTestApp(t)
	_ = a.config.update(context.Background(), func(c *config) error { c.Network = "ethereum-mainnet"; return nil })
	envNetwork := "ethereum-sepolia"
	a.getenv = func(name string) string {
		switch name {
		case "NODIT_API_KEY":
			return "test-api-key"
		case "NODIT_NETWORK":
			return envNetwork
		}
		return ""
	}
	var calls atomic.Int32
	wantPath := "/v1/ethereum/hoodi/native/getNativeBalanceByAccount"
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path != wantPath {
			t.Errorf("wrong network: %s", r.URL.Path)
		}
		return response(200, `{"balance":"0"}`), nil
	})
	args := []string{"data", "native", "balance", "--address", testAddress}
	if c, _, e := run(t, a, append(append([]string{}, args...), "-n", "ethereum-hoodi")...); c != 0 {
		t.Fatal(e)
	}
	wantPath = "/v1/ethereum/sepolia/native/getNativeBalanceByAccount"
	if c, _, e := run(t, a, args...); c != 0 {
		t.Fatal(e)
	}
	envNetwork = ""
	wantPath = "/v1/ethereum/mainnet/native/getNativeBalanceByAccount"
	if c, _, e := run(t, a, args...); c != 0 {
		t.Fatal(e)
	}
	for _, invalidArgs := range [][]string{
		{"data", "native", "balance", "--address", testAddress, "-n", "unknown-network"},
		{"data", "native", "balance", "--address", testAddress, "-n", "solana-devnet"},
		{"data", "native", "balance", "--address", "0xnot-an-address"},
		{"data", "native", "balance"},
		{"rpc", "eth_getBalance", "--params", "{}"},
		{"rpc", "eth_getBalance", "--params", "null"},
		{"rpc", "eth_getBalance", "--params", "[1] {}"},
		{"rpc", "eth_getBalance", "--params", "[NaN]"},
	} {
		if c, out, e := run(t, a, invalidArgs...); c != 2 || out != "" {
			t.Fatalf("%v: %d %s %s", invalidArgs, c, out, e)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("invalid arguments reached the API")
	}
}

func TestRPCEnvelopeErrorsAndPrecision(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "sensitive-api-key"
		}
		return ""
	}
	wantParams := "[]"
	reply := `{"jsonrpc":"2.0","id":1,"result":"0x123abc"}`
	var calls atomic.Int32
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "ethereum-mainnet."+a.env.Domain || r.URL.Path != "/" || r.Header.Get("Authorization") != "" {
			t.Fatal("wrong RPC target")
		}
		var body struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.JSONRPC != "2.0" || body.ID != 1 || body.Method != "eth_getBalance" || string(body.Params) != wantParams {
			t.Fatalf("wrong RPC body: %+v", body)
		}
		return response(200, reply), nil
	})
	args := []string{"rpc", "eth_getBalance", "-n", "ethereum-mainnet", "-o", "json"}
	if c, out, e := run(t, a, args...); c != 0 || !strings.Contains(out, `"result": "0x123abc"`) {
		t.Fatalf("%d %s %s", c, out, e)
	}
	wantParams = `[90071992547409931234567890,"123"]`
	args = append(args, "--params", wantParams)
	reply = `{"jsonrpc":"2.0","id":1,"result":{"big":90071992547409931234567890,"text":"123"}}`
	if c, out, e := run(t, a, args...); c != 0 || !strings.Contains(out, `90071992547409931234567890`) || !strings.Contains(out, `"text": "123"`) {
		t.Fatalf("%d %s %s", c, out, e)
	}
	reply = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid sensitive-api-key","data":{"access_token":"do-not-print","large":90071992547409931234567890}}}`
	if c, out, e := run(t, a, args...); c != 1 || out != "" || !strings.Contains(e, `"apiCode": -32602`) || !strings.Contains(e, `"httpStatus": 200`) || strings.Contains(e, "sensitive-api-key") || strings.Contains(e, "do-not-print") {
		t.Fatalf("%d %s %s", c, out, e)
	}
	for _, bad := range []string{`{"result":1}`, `{"jsonrpc":"2.0","id":2,"result":1}`, `{"jsonrpc":"2.0","id":1,"error":{}}`, `{"jsonrpc":"2.0","id":1,"result":1,"error":{"code":1,"message":"bad"}}`} {
		reply = bad
		if c, _, e := run(t, a, args...); c != 1 || !strings.Contains(e, "INVALID_API_RESPONSE") {
			t.Fatalf("%d %s", c, e)
		}
	}
	if calls.Load() != 7 {
		t.Fatalf("unexpected retries: %d", calls.Load())
	}
}

func TestRPCXRPLEnvelope(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "sensitive-api-key"
		}
		return ""
	}
	reply := `{"result":{"ledger_current_index":107020555,"status":"success"}}`
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "xrpl-mainnet."+a.env.Domain {
			t.Fatal("wrong RPC target")
		}
		return response(200, reply), nil
	})
	args := []string{"rpc", "ledger_current", "-n", "xrpl-mainnet", "-o", "json"}

	// Success carries no jsonrpc or id, so the 2.0 envelope check cannot apply.
	if c, out, e := run(t, a, args...); c != 0 || !strings.Contains(out, `"ledger_current_index": 107020555`) || e != "" {
		t.Fatalf("%d %s %s", c, out, e)
	}

	// An application error is HTTP 200 with the failure inside result.
	reply = `{"result":{"error":"actMalformed","error_code":35,"error_message":"Account malformed.","status":"error"}}`
	if c, out, e := run(t, a, args...); c != 1 || out != "" ||
		!strings.Contains(e, "Account malformed.") || !strings.Contains(e, `"apiCode": 35`) {
		t.Fatalf("%d %s %s", c, out, e)
	}

	// Only the error name is present, so it stands in as the message.
	reply = `{"result":{"error":"actMalformed","status":"error"}}`
	if c, _, e := run(t, a, args...); c != 1 || !strings.Contains(e, "actMalformed") {
		t.Fatalf("%d %s", c, e)
	}

	// An unknown method is the one case XRPL answers with a 2.0 error envelope, still without an id.
	reply = `{"jsonrpc":"2.0","error":{"code":-32601,"message":"the method no_such does not exist"}}`
	if c, _, e := run(t, a, args...); c != 1 || !strings.Contains(e, `"apiCode": -32601`) || !strings.Contains(e, "RPC_ERROR") {
		t.Fatalf("%d %s", c, e)
	}

	for _, bad := range []string{`{"result":1}`, `{}`, `{"result":{"status":"error"}}`, `{"error":{}}`} {
		reply = bad
		if c, _, e := run(t, a, args...); c != 1 || !strings.Contains(e, "INVALID_API_RESPONSE") {
			t.Fatalf("%d %s", c, e)
		}
	}
}

func TestRPCParameterInputs(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "test-product-key"
		}
		return ""
	}
	want := "[]"
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		var body struct {
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body.Params) != want {
			t.Fatalf("params: got %s, want %s", body.Params, want)
		}
		return response(200, `{"jsonrpc":"2.0","id":1,"result":null}`), nil
	})

	want = `["0x1234","latest",true,{"commitment":"confirmed"}]`
	if c, _, e := run(t, a, "rpc", "eth_getBalance", "0x1234", "latest", "true", `{"commitment":"confirmed"}`, "-n", "ethereum-mainnet"); c != 0 {
		t.Fatal(e)
	}

	want = `[90071992547409931234567890,"123"]`
	if c, _, e := run(t, a, "rpc", "eth_getBalance", "--params", want, "-n", "ethereum-mainnet"); c != 0 {
		t.Fatal(e)
	}

	file := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(file, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	if c, _, e := run(t, a, "rpc", "eth_getBalance", "--params-file", file, "-n", "ethereum-mainnet"); c != 0 {
		t.Fatal(e)
	}

	a.stdin = strings.NewReader(want)
	a.stdinRedirected = true
	if c, _, e := run(t, a, "rpc", "eth_getBalance", "-n", "ethereum-mainnet"); c != 0 {
		t.Fatal(e)
	}

	for _, args := range [][]string{
		{"rpc", "eth_getBalance", "{bad", "-n", "ethereum-mainnet"},
		{"rpc", "eth_getBalance", "latest", "--params", "[]", "-n", "ethereum-mainnet"},
		{"rpc", "eth_getBalance", "--params", "[]", "--params-file", file, "-n", "ethereum-mainnet"},
	} {
		a.stdinRedirected = false
		if c, out, _ := run(t, a, args...); c != 2 || out != "" {
			t.Fatalf("%v: %d %s", args, c, out)
		}
	}
}

func TestRejectedCredentialNamesTheWayOut(t *testing.T) {
	for _, tc := range []struct {
		status int
		routes bool
	}{{401, true}, {403, true}, {429, false}, {503, false}} {
		a := newTestApp(t)
		a.getenv = func(name string) string {
			if name == "NODIT_API_KEY" {
				return "sensitive-api-key"
			}
			return ""
		}
		mockAPI(a, func(*http.Request) (*http.Response, error) {
			return response(tc.status, `{"message":"Authentication failed."}`), nil
		})
		_, _, e := run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet")
		// The server wording is kept; the routes out are what the CLI adds to it.
		if !strings.Contains(e, "Authentication failed.") {
			t.Fatalf("%d dropped the server message: %s", tc.status, e)
		}
		if strings.Contains(e, "nodit auth status") != tc.routes {
			t.Fatalf("%d routing %v: %s", tc.status, tc.routes, e)
		}
		if tc.status == 401 {
			for _, want := range []string{"NODIT_API_KEY", "nodit auth login", "nodit project select"} {
				if !strings.Contains(e, want) {
					t.Fatalf("401 missing %q: %s", want, e)
				}
			}
		}
	}
}

func TestAPIErrorMappingAndNoCredentialLeak(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{401, `{"code":"AUTHENTICATION_FAILED","message":"bad sensitive-api-key"}`, "AUTHENTICATION_FAILED"},
		{403, `{"code":"PERMISSION_DENIED","message":"denied"}`, "PERMISSION_DENIED"},
		{403, `{"code":"PLAN_NOT_SUPPORTED","message":"plan unavailable"}`, "PLAN_NOT_SUPPORTED"},
		{429, `{"code":"TOO_MANY_REQUESTS","message":"slow down"}`, "TOO_MANY_REQUESTS"},
		{503, `<html>upstream sensitive-api-key</html>`, "API_ERROR"},
		{200, `{"balance":0} trailing`, "INVALID_API_RESPONSE"},
	} {
		a := newTestApp(t)
		a.getenv = func(name string) string {
			if name == "NODIT_API_KEY" {
				return "sensitive-api-key"
			}
			return ""
		}
		calls := 0
		mockAPI(a, func(*http.Request) (*http.Response, error) { calls++; return response(tc.status, tc.body), nil })
		c, out, e := run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet", "-o", "json")
		if c != 1 || out != "" || strings.Contains(e, "sensitive-api-key") || !strings.Contains(e, tc.code) || calls != 1 {
			t.Fatalf("%d %s %s", c, out, e)
		}
		var result struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal([]byte(e), &result); err != nil {
			t.Fatal(err)
		}
		if _, ok := result.Error["hint"]; ok {
			t.Fatal("error output contains hint")
		}
		if _, ok := result.Error["retryable"]; ok {
			t.Fatal("error output contains retryable")
		}
	}
}

func TestRateLimitErrorKeepsRetryDetails(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "sensitive-api-key"
		}
		return ""
	}
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		r := response(429, `{"code":"RATE_LIMITED","message":"Rate limit of 60 requests per minute exceeded"}`)
		r.Header.Set("Retry-After", "60")
		r.Header.Set("X-RateLimit-Limit", "60")
		r.Header.Set("X-RateLimit-Remaining", "0")
		r.Header.Set("X-RateLimit-Reset", "1789620193")
		return r, nil
	})
	c, out, e := run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet", "-o", "json")
	if c != 1 || out != "" {
		t.Fatalf("%d %s %s", c, out, e)
	}
	var result struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(e), &result); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]float64{
		"retryAfter": 60, "rateLimitLimit": 60, "rateLimitRemaining": 0, "rateLimitReset": 1789620193,
	} {
		if got, ok := result.Error.Details[field].(float64); !ok || got != want {
			t.Fatalf("%s: %v", field, result.Error.Details[field])
		}
	}
}

func TestServerDetailsReachTheErrorOutput(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "sensitive-api-key"
		}
		return ""
	}
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		return response(400, `{"code":"PLAN_RANGE_EXCEEDED","message":"plan reads 30 days",`+
			`"details":{"lookbackDays":30,"earliestFrom":"2026-07-11T05:34:30Z"}}`), nil
	})
	c, out, e := run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet", "-o", "json")
	if c != 1 || out != "" {
		t.Fatalf("%d %s %s", c, out, e)
	}
	var result struct {
		Error struct {
			APICode string         `json:"apiCode"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(e), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error.APICode != "PLAN_RANGE_EXCEEDED" || result.Error.Details["earliestFrom"] != "2026-07-11T05:34:30Z" {
		t.Fatalf("%+v", result.Error)
	}
}

func TestNonRateLimitErrorOmitsDetails(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(string) string { return "" }
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		// The gateway sends these on every response, including ones that are not rate limited.
		r := response(404, `{"code":"NOT_FOUND","message":"missing"}`)
		r.Header.Set("X-RateLimit-Limit", "60")
		r.Header.Set("X-RateLimit-Remaining", "59")
		return r, nil
	})
	c, out, e := run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet", "--api-key", "k", "-o", "json")
	if c != 1 || out != "" {
		t.Fatalf("%d %s %s", c, out, e)
	}
	var result struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(e), &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Error["details"]; ok {
		t.Fatalf("details present without retry headers: %s", e)
	}
}

func TestSuccessWithoutBodyIsNotAnInvalidResponse(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		a := newTestApp(t)
		a.getenv = func(name string) string {
			if name == "NODIT_API_KEY" {
				return "api-key"
			}
			return ""
		}
		mockAPI(a, func(*http.Request) (*http.Response, error) { return response(status, ""), nil })
		if code, _, stderr := run(t, a, "webhook", "classic", "delete", "5001", "-n", "ethereum-mainnet", "--yes"); code != 0 || stderr != "" {
			t.Fatalf("status %d: %d %s", status, code, stderr)
		}
	}
}

func TestAPIRequiresKeyWithoutOAuthFallback(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_AUTH_TOKEN" {
			return "oauth-only"
		}
		return ""
	}
	_ = a.saveSession(&session{"oauth-access", "oauth-refresh", time.Now().Add(time.Hour), a.env.Issuer, a.env.Resource})
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		t.Fatal("request without API key")
		return nil, errors.New("unexpected")
	})
	if c, out, e := run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet"); c != 1 || out != "" || !strings.Contains(e, "API_KEY_REQUIRED") {
		t.Fatalf("%d %s %s", c, out, e)
	}
}

func TestAPITransportRedirectAndTimeout(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	defer close(release)
	a := newTestApp(t)
	a.httpClient = server.Client()
	if _, err := a.apiPost(context.Background(), server.URL, "sensitive-api-key", map[string]int{"id": 1}); err == nil {
		t.Fatal("redirect accepted")
	}
	if redirected.Load() != 0 {
		t.Fatal("API key forwarded to redirect")
	}
	a.timeoutMS = 10
	_, err := a.apiPost(context.Background(), server.URL+"/slow", "sensitive-api-key", map[string]int{"id": 1})
	var ce *commandError
	if !errors.As(err, &ce) || ce.Code != "TIMEOUT" || !strings.Contains(ce.Message, "--timeout") {
		t.Fatalf("unexpected timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.apiPost(ctx, server.URL, "sensitive-api-key", map[string]int{"id": 1}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	// A URL embedded in a transport error must never reach stderr.
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: "https://host/sensitive-api-key", Err: errors.New("sensitive-api-key")}
	})
	if _, err := a.apiPost(context.Background(), server.URL, "sensitive-api-key", map[string]int{"id": 1}); strings.Contains(err.Error(), "sensitive-api-key") {
		t.Fatal("transport error leaked credential")
	}
}
