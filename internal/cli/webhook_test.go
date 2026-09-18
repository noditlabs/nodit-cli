package cli

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassicAndFlexibleWebhookRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, query, body string
		args                            []string
	}{
		{"classic-list", "GET", "/v1/ethereum/mainnet/webhooks", "page=2&rpp=20", "", []string{"classic", "list", "--page", "2", "--rpp", "20"}},
		{"classic-get", "GET", "/v1/ethereum/mainnet/webhooks", "subscriptionId=5001", "", []string{"classic", "get", "5001"}},
		{"classic-create", "POST", "/v1/ethereum/mainnet/webhooks", "", `{"eventType":"ADDRESS_ACTIVITY","notification":{"url":"https://example.com"},"condition":{"addresses":["` + testAddress + `"]}}`, []string{"classic", "create", "--body", `{"eventType":"ADDRESS_ACTIVITY","notification":{"url":"https://example.com"},"condition":{"addresses":["` + testAddress + `"]}}`}},
		{"classic-update", "PATCH", "/v1/ethereum/mainnet/webhooks/5001", "", `{"description":"changed"}`, []string{"classic", "update", "5001", "--body", `{"description":"changed"}`}},
		{"classic-delete", "DELETE", "/v1/ethereum/mainnet/webhooks/5001", "", "", []string{"classic", "delete", "5001", "--yes"}},
		{"history", "GET", "/v1/ethereum/mainnet/webhooks/history", "page=1&rpp=10&status=SUCCESS&subscriptionId=5001&withEventMessage=true", "", []string{"classic", "history", "5001", "--status", "SUCCESS", "--with-event-message"}},
		{"flex-list", "GET", "/v1/ethereum/mainnet/flexible-webhooks", "page=1&rpp=10", "", []string{"flexible", "list"}},
		{"flex-get", "GET", "/v1/ethereum/mainnet/flexible-webhooks/9001", "", "", []string{"flexible", "get", "9001"}},
		{"flex-create", "POST", "/v1/ethereum/mainnet/flexible-webhooks", "", `{"name":"n","streamId":"77","filterExpression":"bigint_gt(value, \"0\")","destination":"https://example.com"}`, []string{"flexible", "create", "--body", `{"name":"n","streamId":"77","filterExpression":"bigint_gt(value, \"0\")","destination":"https://example.com"}`}},
		{"flex-update", "PATCH", "/v1/ethereum/mainnet/flexible-webhooks/9001", "", `{"status":"PAUSED"}`, []string{"flexible", "update", "9001", "--body", `{"status":"PAUSED"}`}},
		{"flex-delete", "DELETE", "/v1/ethereum/mainnet/flexible-webhooks/9001", "", "", []string{"flexible", "delete", "9001", "--yes"}},
		{"streams", "GET", "/v1/ethereum/mainnet/flexible-webhooks/streams", "page=2&rpp=100", "", []string{"flexible", "streams", "--page", "2", "--rpp", "100"}},
		{"schema", "GET", "/v1/ethereum/mainnet/flexible-webhooks/streams/77/schema", "", "", []string{"flexible", "schema", "77"}},
		{"addresses-list", "GET", "/v1/ethereum/mainnet/webhooks/5001/addresses", "order=desc&page=2&search=0xabc%2B%26&size=1000&sort=address", "", []string{"classic", "addresses", "list", "5001", "--page", "2", "--size", "1000", "--sort", "address", "--order", "desc", "--search", "0xabc+&"}},
		{"addresses-update", "PATCH", "/v1/ethereum/mainnet/webhooks/5001/addresses", "", `{"add":["` + testAddress + `"],"remove":["0x0000000000000000000000000000000000000001"]}`, []string{"classic", "addresses", "update", "5001", "--body", `{"add":["` + testAddress + `"],"remove":["0x0000000000000000000000000000000000000001"]}`, "--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := productTestApp(t)
			mockAPI(a, func(r *http.Request) (*http.Response, error) {
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != tc.query || r.Header.Get("X-API-KEY") != "test-product-key" {
					t.Fatalf("wrong request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
				}
				if tc.body != "" {
					assertBody(t, r, tc.body)
				} else if r.Body != nil {
					content, _ := io.ReadAll(r.Body)
					if len(content) != 0 {
						t.Fatal("unexpected body")
					}
				}
				return response(200, `{"subscriptionId":"9007199254740993","signingKey":"shown-once"}`), nil
			})
			args := append([]string{"webhook"}, tc.args...)
			args = append(args, "--network", "ethereum-mainnet", "--output", "json")
			code, out, stderr := run(t, a, args...)
			wantSigningKey := !strings.HasPrefix(tc.name, "flex-") || tc.name == "flex-create"
			if code != 0 || stderr != "" || strings.Contains(out, "signingKey") != wantSigningKey {
				t.Fatalf("%d %s %s", code, out, stderr)
			}
		})
	}
}

func TestWebhookValidationBeforeRequest(t *testing.T) {
	a := productTestApp(t)
	mockAPI(a, func(*http.Request) (*http.Response, error) { t.Fatal("invalid webhook reached API"); return nil, nil })
	for _, args := range [][]string{
		{"webhook", "classic", "create", "--body", `{}`},
		{"webhook", "classic", "update", "a/b", "--body", `{}`, "-n", "ethereum-mainnet"},
		{"webhook", "classic", "delete", "1", "-n", "ethereum-mainnet", "--no-interactive"},
		{"webhook", "classic", "history", "1", "-n", "ethereum-mainnet", "--status", "UNKNOWN"},
		{"webhook", "flexible", "create", "-n", "ethereum-mainnet", "--body", `{"name":"n"}`},
		{"webhook", "flexible", "create", "-n", "ethereum-mainnet", "--body", `{"name":"n","streamId":"1","filterExpression":"true","destination":{},"unknown":1}`},
		{"webhook", "flexible", "update", "1", "-n", "ethereum-mainnet", "--body", `{"status":"STOPPED"}`},
		{"webhook", "flexible", "update", "1", "-n", "ethereum-mainnet", "--body", `{}`},
		{"webhook", "classic", "addresses", "list", "1", "-n", "ethereum-mainnet", "--size", "1001"},
		{"webhook", "classic", "addresses", "list", "1", "-n", "ethereum-mainnet", "--sort", "updatedAt"},
		{"webhook", "classic", "addresses", "update", "1", "-n", "ethereum-mainnet", "--body", `{}`},
		{"webhook", "classic", "addresses", "update", "1", "-n", "ethereum-mainnet", "--body", `{"add":[],"remove":[]}`},
		{"webhook", "classic", "addresses", "update", "1", "-n", "ethereum-mainnet", "--body", `{"unknown":[]}`},
		{"webhook", "classic", "addresses", "update", "1", "-n", "ethereum-mainnet", "--body", `{"remove":["` + testAddress + `"]}`, "--no-interactive"},
		{"webhook", "classic", "list", "-n", "solana-devnet"},
	} {
		code, out, _ := run(t, a, args...)
		if code != 1 && code != 2 || out != "" {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
}

func TestWebhookConfirmation(t *testing.T) {
	a := productTestApp(t)
	a.stdin = strings.NewReader("no\n")
	if code, out, stderr := run(t, a, "webhook", "classic", "delete", "1", "-n", "ethereum-mainnet"); code != 1 || out != "" || !strings.Contains(stderr, "CANCELLED") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	a.stdin = strings.NewReader("yes\n")
	mockAPI(a, func(r *http.Request) (*http.Response, error) { return response(200, `{"deleted":true}`), nil })
	if code, _, stderr := run(t, a, "webhook", "classic", "delete", "1", "-n", "ethereum-mainnet"); code != 0 || stderr == "" {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestAddressExportIsAtomicAndDoesNotOverwrite(t *testing.T) {
	a := productTestApp(t)
	target := filepath.Join(t.TempDir(), "addresses.csv")
	calls := 0
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v1/ethereum/mainnet/webhooks/5001/addresses/download" || r.URL.RawQuery != "order=asc&search=0xabc&sort=createdAt" || r.Header.Get("Accept") != "text/csv" {
			t.Fatalf("wrong export request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/csv"}}, Body: io.NopCloser(strings.NewReader("address,createdAt\n" + testAddress + ",2026-09-14T00:00:00Z\n"))}, nil
	})
	args := []string{"webhook", "classic", "addresses", "export", "5001", "-n", "ethereum-mainnet", "--file", target, "--search", "0xabc", "-o", "json"}
	if code, out, stderr := run(t, a, args...); code != 0 || stderr != "" || !strings.Contains(out, `"format": "csv"`) {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	content, err := os.ReadFile(target)
	if err != nil || !strings.HasPrefix(string(content), "address,createdAt\n") {
		t.Fatal(err)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if code, _, stderr := run(t, a, args...); code != 1 || !strings.Contains(stderr, "FILE_EXISTS") || calls != 1 {
		t.Fatalf("%d %d %s", code, calls, stderr)
	}
	content2, _ := os.ReadFile(target)
	if string(content2) != string(content) {
		t.Fatal("existing file changed")
	}
}
