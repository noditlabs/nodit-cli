package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const testKeyID = "3f2b1a90-77c4-4e2e-9a11-8d0b6c5e4f21"

func managementTestApp(t *testing.T) *app {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_AUTH_TOKEN" {
			return "management-token"
		}
		return ""
	}
	return a
}

func TestManagementReadCommands(t *testing.T) {
	for _, tc := range []struct {
		path, query string
		args        []string
	}{
		{"/v1/projects", "", []string{"project", "list"}},
		{"/v1/chains", "network=mainnet&page=2&projectId=123&protocol=ethereum&rpp=1000", []string{"project", "networks", "--project", "123", "--protocol", "ethereum", "--network", "mainnet", "--page", "2", "--rpp", "1000"}},
		{"/v1/api-keys", "page=1&projectId=123&rpp=20&status=ACTIVE", []string{"apikey", "list", "--project", "123", "--status", "ACTIVE"}},
		{"/v1/api-keys", "page=1&rpp=20&status=ACTIVE", []string{"apikey", "list"}},
		{"/v1/api-keys", "page=1&rpp=20", []string{"apikey", "list", "--all"}},
		{"/v1/api-keys", "page=1&rpp=20&status=DELETED", []string{"apikey", "list", "--status", "DELETED"}},
		{"/v1/usage/summary", "period=24h&projectId=123&requestType=NODE_API&requestType=WEB3_DATA_API", []string{"usage", "summary", "--project", "123", "--period", "24h", "--request-type", "NODE_API,WEB3_DATA_API"}},
		{"/v1/usage/timeseries", "from=2026-09-01T00%3A00%3A00Z&granularity=1h&to=2026-09-02T00%3A00%3A00Z", []string{"usage", "timeseries", "--from", "2026-09-01T00:00:00Z", "--to", "2026-09-02T00:00:00Z", "--granularity", "1h"}},
		{"/v1/usage/breakdown", "groupBy=PROJECT&groupBy=CHAIN&page=2&rpp=50", []string{"usage", "breakdown", "--group-by", "PROJECT,CHAIN", "--page", "2", "--rpp", "50"}},
		{"/v1/allowlist", "projectId=123", []string{"allowlist", "list", "--project", "123"}},
	} {
		a := managementTestApp(t)
		mockAPI(a, func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.Host != "api.test.example.test" || r.URL.Path != tc.path || r.URL.RawQuery != tc.query || r.Header.Get("Authorization") != "Bearer management-token" || r.Header.Get("X-API-KEY") != "" {
				t.Fatalf("wrong request: %s %s", r.Method, r.URL)
			}
			return response(200, `{"usedCu":"9007199254740993","requests":null,"items":[]}`), nil
		})
		code, out, stderr := run(t, a, append(tc.args, "--output", "json")...)
		if code != 0 || stderr != "" || !strings.Contains(out, `"usedCu": "9007199254740993"`) {
			t.Fatalf("%d %s %s", code, out, stderr)
		}
	}
}

func TestAPIKeyGetAlwaysMasksValue(t *testing.T) {
	a := managementTestApp(t)
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		return response(200, `{"keyId":"`+testKeyID+`","projectId":"123","maskedValue":"abc...xyz","value":"plain-secret","status":"ACTIVE"}`), nil
	})
	code, out, stderr := run(t, a, "apikey", "get", testKeyID, "-o", "json")
	if code != 0 || stderr != "" || strings.Contains(out+stderr, "plain-secret") || !strings.Contains(out, "maskedValue") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
}

func TestAPIKeyListAlwaysMasksValue(t *testing.T) {
	a := managementTestApp(t)
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		return response(200, `{"count":1,"page":1,"rpp":20,"items":[{"keyId":"`+testKeyID+`","maskedValue":"abc...xyz","value":"plain-secret"}]}`), nil
	})
	code, out, stderr := run(t, a, "apikey", "list", "-o", "json")
	if code != 0 || stderr != "" || strings.Contains(out+stderr, "plain-secret") || !strings.Contains(out, "maskedValue") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
}

func TestProjectSelectLinksValidatedKeyAndProductUsesIt(t *testing.T) {
	a := managementTestApp(t)
	calls := 0
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Path {
		case "/v1/projects":
			if r.URL.Query().Get("projectId") != "123" {
				t.Fatal(r.URL)
			}
			return response(200, `{"count":1,"items":[{"projectId":"123","status":"RUNNING"}]}`), nil
		case "/v1/api-keys":
			if r.URL.Query().Get("status") != "ACTIVE" || r.URL.Query().Get("rpp") != "1000" {
				t.Fatal(r.URL)
			}
			return response(200, `{"count":1,"page":1,"rpp":1000,"items":[{"keyId":"`+testKeyID+`","projectId":"123","status":"ACTIVE"}]}`), nil
		case "/v1/api-keys/" + testKeyID:
			return response(200, `{"keyId":"`+testKeyID+`","projectId":"123","status":"ACTIVE","value":"linked-product-key"}`), nil
		default:
			if r.URL.Path != "/v1/ethereum/mainnet/native/getNativeBalanceByAccount" || r.Header.Get("X-API-KEY") != "linked-product-key" || r.Header.Get("Authorization") != "" {
				t.Fatalf("wrong product request: %s", r.URL)
			}
			return response(200, `{"balance":"1"}`), nil
		}
	})
	code, out, stderr := run(t, a, "project", "select", "123", "-o", "json")
	if code != 0 || stderr != "" || strings.Contains(out, "linked-product-key") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	c, err := a.config.read()
	if err != nil || c.Project != "123" || c.ProjectKeys["123"] != testKeyID {
		t.Fatalf("%+v %v", c, err)
	}
	a.getenv = func(string) string { return "" }
	if code, _, stderr = run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet"); code != 0 {
		t.Fatal(stderr)
	}
	if calls != 4 {
		t.Fatal(calls)
	}
	if code, _, stderr = run(t, a, "config", "unset", "project"); code != 0 {
		t.Fatal(stderr)
	}
	c, _ = a.config.read()
	if c.Project != "" || c.ProjectKeys["123"] != testKeyID {
		t.Fatalf("unset removed link: %+v", c)
	}
}

func TestProjectSelectFailurePreservesSelection(t *testing.T) {
	a := managementTestApp(t)
	if err := a.config.update(context.Background(), func(c *config) error {
		c.Project = "111"
		c.ProjectKeys = map[string]string{"111": testKeyID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = a.keys.Set(projectCredentialKey("111", testKeyID), "old-key")
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/projects" {
			return response(200, `{"count":1,"items":[{"projectId":"222","status":"RUNNING"}]}`), nil
		}
		return response(200, `{"keyId":"`+testKeyID+`","projectId":"other","status":"ACTIVE","value":"new-key"}`), nil
	})
	code, out, _ := run(t, a, "project", "select", "222", "--key-id", testKeyID)
	if code != 1 || out != "" {
		t.Fatal(code, out)
	}
	c, _ := a.config.read()
	old, _ := a.keys.Get(projectCredentialKey("111", testKeyID))
	if c.Project != "111" || old != "old-key" {
		t.Fatalf("state changed: %+v", c)
	}
}

func TestManagementMutations(t *testing.T) {
	for _, tc := range []struct {
		method, path, query, body string
		args                      []string
	}{
		{"PATCH", "/v1/allowlist", "projectId=123", `{"domain":{"restrict":false},"ip":{"restrict":true},"matchRule":"OR"}`, []string{"allowlist", "set", "--project", "123", "--ip-restrict", "--domain-restrict=false", "--match-rule", "OR"}},
		{"POST", "/v1/allowlist/ips", "projectId=123", `{"name":"office","value":"192.0.2.1"}`, []string{"allowlist", "add", "192.0.2.1", "--project", "123", "--type", "ip", "--name", "office"}},
		{"DELETE", "/v1/allowlist/domains", "projectId=123&value=example.com", "", []string{"allowlist", "remove", "example.com", "--project", "123", "--type", "domain", "--yes"}},
	} {
		a := managementTestApp(t)
		mockAPI(a, func(r *http.Request) (*http.Response, error) {
			if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != tc.query {
				t.Fatalf("%s %s", r.Method, r.URL)
			}
			if tc.body != "" {
				assertBody(t, r, tc.body)
			}
			if tc.method == "DELETE" {
				return response(204, ""), nil
			}
			return response(200, `{"projectId":"123"}`), nil
		})
		code, _, stderr := run(t, a, tc.args...)
		if code != 0 {
			t.Fatal(stderr)
		}
	}
}

func TestManagementValidationBeforeRequest(t *testing.T) {
	a := managementTestApp(t)
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid management request reached API")
		return nil, nil
	})
	for _, args := range [][]string{
		{"project", "networks", "--network", "mainnet"},
		{"project", "select", "not-a-project"},
		{"apikey", "get", "not-a-uuid"},
		{"apikey", "list", "--status", "PAUSED"},
		{"usage", "summary", "--period", "24h", "--from", "2026-09-01T00:00:00Z"},
		{"usage", "summary", "--period", "0h"},
		{"usage", "summary", "--network", "mainnet"},
		{"usage", "timeseries", "--granularity", "1m"},
		{"usage", "breakdown"},
		{"usage", "breakdown", "--group-by", "PROJECT,PROJECT"},
		{"allowlist", "set", "--project", "123"},
		{"allowlist", "add", "x", "--project", "123", "--type", "unknown"},
	} {
		if code, out, _ := run(t, a, args...); code != 2 || out != "" {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
}

func TestUsageHelpCarriesTheAccountingCaveats(t *testing.T) {
	a := newTestApp(t)
	for _, args := range [][]string{{"usage", "--help"}, {"usage", "summary", "--help"}, {"usage", "timeseries", "--help"}, {"usage", "breakdown", "--help"}} {
		c, out, e := run(t, a, args...)
		if c != 0 || e != "" {
			t.Fatalf("%v %d %s", args, c, e)
		}
		for _, want := range []string{"billing statement", "Dedicated node traffic is excluded", "40 days"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v missing %q", args, want)
			}
		}
	}
}

func TestProjectListKeepsOnlyRunningUnlessAllIsGiven(t *testing.T) {
	body := `{"count":5,"items":[` +
		`{"projectId":"1","name":"live","status":"RUNNING"},` +
		`{"projectId":"2","name":"gone","status":"DELETED"},` +
		`{"projectId":"3","name":"paused","status":"PAUSED"},` +
		`{"projectId":"4","name":"waiting","status":"WAIT"},` +
		`{"projectId":"5","name":"broken","status":"ERROR"}]}`
	for _, tc := range []struct {
		args   []string
		others bool
		count  string
	}{
		{[]string{"project", "list"}, false, `"count": 1`},
		{[]string{"project", "list", "--all"}, true, `"count": 5`},
	} {
		a := managementTestApp(t)
		mockAPI(a, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		code, out, stderr := run(t, a, append(tc.args, "-o", "json")...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v %d %s", tc.args, code, stderr)
		}
		if !strings.Contains(out, `"live"`) || !strings.Contains(out, tc.count) {
			t.Fatalf("%v %s", tc.args, out)
		}
		for _, name := range []string{`"gone"`, `"paused"`, `"waiting"`, `"broken"`} {
			if strings.Contains(out, name) != tc.others {
				t.Fatalf("%v %s: %s", tc.args, name, out)
			}
		}
	}
}

func TestAPIKeyStatusAndAllConflict(t *testing.T) {
	a := managementTestApp(t)
	code, out, stderr := run(t, a, "apikey", "list", "--status", "ACTIVE", "--all", "-o", "json")
	if code != 2 || out != "" || !strings.Contains(stderr, "not both") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
}

func TestProjectSelectListsKeysWhenSeveralAreActive(t *testing.T) {
	a := managementTestApp(t)
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/projects":
			return response(200, `{"count":1,"items":[{"projectId":"123","status":"RUNNING"}]}`), nil
		case "/v1/api-keys":
			return response(200, `{"count":2,"items":[`+
				`{"keyId":"`+testKeyID+`","name":"web","maskedValue":"abc...xyz","status":"ACTIVE"},`+
				`{"keyId":"7c9e6679-7425-40de-944b-e07fc1f90ae7","maskedValue":"def...uvw","status":"ACTIVE"}]}`), nil
		}
		t.Fatalf("unexpected call: %s", r.URL.Path)
		return nil, nil
	})
	c, out, e := run(t, a, "project", "select", "123", "-o", "json")
	if c != 1 || out != "" {
		t.Fatalf("%d %s %s", c, out, e)
	}
	for _, want := range []string{"API_KEY_SELECTION_REQUIRED", "--key-id", "2 active API keys", testKeyID, "abc...xyz", "web"} {
		if !strings.Contains(e, want) {
			t.Fatalf("missing %q: %s", want, e)
		}
	}
}

func TestCredentialErrorsNameTheNextCommand(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(string) string { return "" }
	c, _, e := run(t, a, "data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet", "-o", "json")
	if c != 1 || !strings.Contains(e, "Set NODIT_API_KEY") || !strings.Contains(e, "nodit project select") {
		t.Fatalf("%d %s", c, e)
	}
	if c, _, e := run(t, a, "usage", "summary", "-o", "json"); c != 1 || !strings.Contains(e, "nodit auth login") {
		t.Fatalf("%d %s", c, e)
	}
}
