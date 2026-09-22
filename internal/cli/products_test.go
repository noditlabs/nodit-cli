package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func productTestApp(t *testing.T) *app {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "test-product-key"
		}
		return ""
	}
	return a
}

func assertBody(t *testing.T, r *http.Request, want string) {
	t.Helper()
	var got, expected any
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	if err := d.Decode(&got); err != nil {
		t.Fatal(err)
	}
	d = json.NewDecoder(strings.NewReader(want))
	d.UseNumber()
	if err := d.Decode(&expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("request body: got %#v, want %#v", got, expected)
	}
}

func TestDataPublicContracts(t *testing.T) {
	for _, tc := range []struct {
		name, network, path, body string
		args                      []string
	}{
		{"native-base", "base-mainnet", "native/getNativeBalanceByAccount", `{"accountAddress":"` + testAddress + `"}`, []string{"native", "balance", "--address", testAddress}},
		{"native-bitcoin", "bitcoin-mainnet", "native/getNativeTokenBalanceByAccount", `{"accountAddress":"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"}`, []string{"native", "balance", "--address", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"}},
		{"tokens", "ethereum-mainnet", "token/getTokensOwnedByAccount", `{"accountAddress":"` + testAddress + `","contractAddresses":["` + testAddress + `"],"page":2,"rpp":10,"withCount":true}`, []string{"token", "balances", "--address", testAddress, "--contract", testAddress, "--page", "2", "--rpp", "10", "--with-count"}},
		{"aptos-tokens", "aptos-testnet", "token/getTokensOwnedByAccount", `{"accountAddress":"0x1","cursor":"next+cursor="}`, []string{"token", "balances", "--address", "0x1", "--cursor", "next+cursor="}},
		{"transfers-account-filter", "ethereum-mainnet", "token/getTokenTransfersByAccount", `{"accountAddress":"` + testAddress + `","contractAddresses":["` + testAddress + `"],"relation":"to","fromBlock":"100","toBlock":"200"}`, []string{"token", "transfers", "--address", testAddress, "--contract", testAddress, "--relation", "to", "--from-block", "100", "--to-block", "200"}},
		{"transfers-contract", "tron-mainnet", "token/getTokenTransfersByContract", `{"contractAddress":"TE2RzoSV3wFK99w6J9UnnZ4vLfXYoxvRwP","rpp":1}`, []string{"token", "transfers", "--contract", "TE2RzoSV3wFK99w6J9UnnZ4vLfXYoxvRwP", "--rpp", "1"}},
		{"metadata", "ethereum-mainnet", "token/getTokenContractMetadataByContracts", `{"contractAddresses":["` + testAddress + `"]}`, []string{"token", "metadata", "--contract", testAddress}},
		{"holders", "ethereum-mainnet", "token/getTokenHoldersByContract", `{"contractAddress":"` + testAddress + `","cursor":"c"}`, []string{"token", "holders", "--contract", testAddress, "--cursor", "c"}},
		{"nft-list", "ethereum-mainnet", "nft/getNftsOwnedByAccount", `{"accountAddress":"` + testAddress + `"}`, []string{"nft", "list", "--address", testAddress}},
		{"nft-transfers", "ethereum-mainnet", "nft/getNftTransfersByContract", `{"contractAddress":"` + testAddress + `"}`, []string{"nft", "transfers", "--contract", testAddress}},
		{"xrpl-history", "xrpl-mainnet", "blockchain/getTransactionsByAccount", `{"accountAddress":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","fromLedger":"100","toLedger":"200"}`, []string{"transaction", "list", "--address", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "--from-ledger", "100", "--to-ledger", "200"}},
		{"aptos-version", "aptos-mainnet", "blockchain/getTransactionByVersion", `{"transactionVersion":9007199254740993}`, []string{"transaction", "get", "--id", "9007199254740993"}},
		{"bitcoin-transaction", "bitcoin-mainnet", "blockchain/getTransactionByTransactionId", `{"transactionId":"` + strings.Repeat("1", 64) + `"}`, []string{"transaction", "get", "--id", strings.Repeat("1", 64)}},
		{"transaction-hash", "ethereum-mainnet", "blockchain/getTransactionByHash", `{"transactionHash":"0x` + strings.Repeat("a", 64) + `"}`, []string{"transaction", "get", "--id", "0x" + strings.Repeat("a", 64)}},
		{"block", "ethereum-mainnet", "blockchain/getBlockByHashOrNumber", `{"block":"latest"}`, []string{"block", "get", "--number", "latest"}},
		{"ledger", "xrpl-mainnet", "blockchain/getLedgerByHashOrIndex", `{"ledger":"123456"}`, []string{"block", "get", "--number", "123456"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := productTestApp(t)
			calls := 0
			mockAPI(a, func(r *http.Request) (*http.Response, error) {
				calls++
				chain, net, _ := strings.Cut(tc.network, "-")
				if r.Method != "POST" || r.URL.Host != "web3."+a.env.Domain || r.URL.Path != "/v1/"+chain+"/"+net+"/"+tc.path || r.Header.Get("X-API-KEY") != "test-product-key" || r.Header.Get("Authorization") != "" {
					t.Fatal("incorrect API route or authentication")
				}
				assertBody(t, r, tc.body)
				return response(200, `{"cursor":"next","items":[{"balance":"9007199254740993","height":9007199254740993}]}`), nil
			})
			args := append([]string{"data"}, tc.args...)
			args = append(args, "-n", tc.network, "-o", "json")
			c, out, e := run(t, a, args...)
			if c != 0 || calls != 1 || !strings.Contains(out, `"balance": "9007199254740993"`) || !strings.Contains(out, `"height": 9007199254740993`) || !strings.Contains(out, `"cursor": "next"`) {
				t.Fatalf("%d %s %s", c, out, e)
			}
		})
	}
}

func TestInvalidDataDoesNotReachAPI(t *testing.T) {
	a := productTestApp(t)
	mockAPI(a, func(*http.Request) (*http.Response, error) { t.Fatal("invalid input reached API"); return nil, nil })
	for _, args := range [][]string{
		{"token", "balances", "--address", testAddress, "--page", "0"},
		{"token", "balances", "--address", testAddress, "--page", "101"},
		{"token", "balances", "--address", testAddress, "--rpp", "1001"},
		{"token", "balances", "--address", testAddress, "--cursor", ""},
		{"token", "balances", "--address", testAddress, "--page", "1", "--cursor", "c"},
		{"token", "transfers", "--contract", testAddress, "--relation", "to"},
		{"token", "transfers", "--address", testAddress, "--from-block", "200", "--to-block", "100"},
		{"token", "transfers", "--address", testAddress, "--from-block", "latest"},
		{"token", "transfers", "--address", testAddress, "--from-block", "1", "--to-date", "2026-09-11T00:00:00Z"},
		{"transaction", "list", "--address", testAddress, "--from-date", "2026-09-12T00:00:00Z", "--to-date", "2026-09-11T00:00:00Z"},
		{"transaction", "list", "--address", testAddress, "--from-ledger", "100"},
		{"block", "get"},
		{"block", "get", "--number", "latest", "--id", "0x" + strings.Repeat("1", 64)},
		{"token", "metadata"},
		{"transaction", "get", "--id", "abc"},
	} {
		c, out, e := run(t, a, append(append([]string{"data"}, args...), "-n", "ethereum-mainnet")...)
		if c != 2 || out != "" {
			t.Fatalf("%v: %d %s %s", args, c, out, e)
		}
	}
	for _, args := range [][]string{
		{"data", "native", "balance", "--address", "0x1", "-n", "aptos-mainnet"},
		{"data", "token", "balances", "--address", "0x1", "--contract", "0x1", "-n", "aptos-mainnet"},
		{"data", "nft", "list", "--address", "T1", "-n", "tron-mainnet"},
		{"data", "native", "balance", "--address", testAddress, "-n", "solana-mainnet"},
		{"data", "transaction", "get", "--id", "id", "-n", "tron-mainnet"},
	} {
		if c, out, e := run(t, a, args...); c != 2 || out != "" || !strings.Contains(e, "UNSUPPORTED_OPERATION") {
			t.Fatalf("%v: %d %s %s", args, c, out, e)
		}
	}
}

func TestSolanaCatalogDoesNotAdvertiseData(t *testing.T) {
	n, err := findNetwork("solana-mainnet")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(n.Products, "data") {
		t.Fatalf("solana-mainnet products: %v", n.Products)
	}
}

func TestLuniverseCatalogAdvertisesDataOnly(t *testing.T) {
	n, err := findNetwork("luniverse-mainnet")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(n.Products, []string{"data"}) {
		t.Fatalf("luniverse-mainnet products: %v", n.Products)
	}
}

func TestRPCChainRoutingAcrossEndpoints(t *testing.T) {
	for _, suffix := range []string{"alpha", "beta", "release"} {
		for _, tc := range []struct{ network, method, host string }{
			{"base-mainnet", "eth_blockNumber", "base-mainnet"},
			{"solana-devnet", "getHealth", "solana-devnet"},
			{"sui-mainnet", "sui_getLatestCheckpointSequenceNumber", "sui-mainnet"},
			{"cosmos-mainnet", "status", "rpc-cosmos-mainnet"},
			{"sei-mainnet", "status", "rpc-sei-mainnet"},
			{"sei-mainnet", "eth_chainId", "evm-sei-mainnet"},
			{"injective-mainnet", "eth_blockNumber", "evm-injective-mainnet"},
		} {
			a := productTestApp(t)
			a.env = testEnvironment(t, suffix)
			mockAPI(a, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != tc.host+"."+a.env.Domain {
					t.Fatal(r.URL.Host)
				}
				assertBody(t, r, `{"jsonrpc":"2.0","id":1,"method":"`+tc.method+`","params":[]}`)
				return response(200, `{"jsonrpc":"2.0","id":1,"result":null}`), nil
			})
			if c, _, e := run(t, a, "rpc", tc.method, "-n", tc.network); c != 0 {
				t.Fatal(e)
			}
		}
	}
}

func TestAptosEventsByType(t *testing.T) {
	a := productTestApp(t)
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/aptos/mainnet/blockchain/getEventsByType" {
			t.Fatal(r.URL)
		}
		assertBody(t, r, `{"eventType":"0x1::fungible_asset::Withdraw","rpp":1000}`)
		return response(200, `{"items":[]}`), nil
	})
	code, _, stderr := run(t, a, "data", "event", "by-type", "--event-type", "0x1::fungible_asset::Withdraw", "--rpp", "1000", "-n", "aptos-mainnet")
	if code != 0 {
		t.Fatal(stderr)
	}
}

func TestRESTContractAndHeaders(t *testing.T) {
	for _, tc := range []struct{ network, path, host, wantPath string }{
		{"aptos-testnet", "/accounts/0x1", "aptos-testnet", "/v1/accounts/0x1"},
		{"aptos-mainnet", "/", "aptos-mainnet", "/v1"},
		{"cosmos-mainnet", "/cosmos/bank/v1beta1/balances/cosmos1abc", "rest-cosmos-mainnet", "/cosmos/bank/v1beta1/balances/cosmos1abc"},
		{"sei-mainnet", "/status", "rpc-sei-mainnet", "/status"},
		{"initia-mainnet", "/initia/move/v1/accounts/0x1/resources", "rest-initia-mainnet", "/initia/move/v1/accounts/0x1/resources"},
		{"tron-mainnet", "/wallet/getnowblock", "tron-mainnet", "/wallet/getnowblock"},
		{"tron-mainnet", "/walletsolidity/getaccount", "tron-mainnet", "/walletsolidity/getaccount"},
	} {
		a := productTestApp(t)
		mockAPI(a, func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.Host != tc.host+"."+a.env.Domain || r.URL.Path != tc.wantPath {
				t.Fatalf("wrong route: %s %s", r.Method, r.URL)
			}
			if !reflect.DeepEqual(r.URL.Query()["cursor"], []string{"+/= &", "second"}) {
				t.Fatal(r.URL.RawQuery)
			}
			if r.Header.Get("Content-Type") != "" {
				t.Fatal("content type without body")
			}
			reply := response(200, `{"ledger_version":"9007199254740993"}`)
			reply.Header.Set("X-Aptos-Ledger-Version", "9007199254740993")
			reply.Header.Set("X-Cursor", "next")
			reply.Header.Set("Set-Cookie", "sensitive-cookie")
			reply.Header.Set("Authorization", "sensitive-token")
			return reply, nil
		})
		c, out, e := run(t, a, "rest", "get", tc.path, "-n", tc.network, "--query", "cursor=+/= &", "--query", "cursor=second", "-o", "json")
		if c != 0 || !strings.Contains(out, `"body"`) || !strings.Contains(out, "X-Aptos-Ledger-Version") || !strings.Contains(out, "X-Cursor") || strings.Contains(out, "sensitive") {
			t.Fatalf("%d %s %s", c, out, e)
		}
	}
	a := productTestApp(t)
	file := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(file, []byte(`{"arguments":[9007199254740993,"42"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" {
			t.Fatal(r.Method)
		}
		assertBody(t, r, `{"arguments":[9007199254740993,"42"]}`)
		return response(204, ""), nil
	})
	c, out, e := run(t, a, "rest", "POST", "/view", "-n", "aptos-mainnet", "--body-file", file, "-o", "json")
	if c != 0 || !strings.Contains(out, `"body": null`) {
		t.Fatalf("%d %s %s", c, out, e)
	}
	a.stdin = strings.NewReader(`{"arguments":[9007199254740993,"42"]}`)
	a.stdinRedirected = true
	c, out, e = run(t, a, "rest", "POST", "/view", "-n", "aptos-mainnet", "-o", "json")
	if c != 0 || !strings.Contains(out, `"body": null`) {
		t.Fatalf("%d %s %s", c, out, e)
	}
	// A GET carries no body, so inherited stdin, such as a pipe in a script, is not read as one.
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.Header.Get("Content-Type") != "" {
			t.Fatalf("stdin reached a GET: %s", r.Method)
		}
		return response(200, `{}`), nil
	})
	a.stdin = strings.NewReader("not json at all")
	c, out, e = run(t, a, "rest", "GET", "/accounts/0x1", "-n", "aptos-mainnet", "-o", "json")
	if c != 0 || !strings.Contains(out, `"body"`) {
		t.Fatalf("%d %s %s", c, out, e)
	}
}

func TestRESTRejectsAmbiguityAndUnsafePaths(t *testing.T) {
	a := productTestApp(t)
	mockAPI(a, func(*http.Request) (*http.Response, error) { t.Fatal("invalid REST reached API"); return nil, nil })
	for _, path := range []string{"https://example.com/x", "//example.com/x", "/accounts?x=1", "/accounts?", "/accounts#", "/../x", "/%2e%2e/x", "/%252e%252e/x", "/%2f%2fexample.com", "/accounts%3fvalue", "/v1/accounts/0x1", "/graphql"} {
		if c, out, e := run(t, a, "rest", "GET", path, "-n", "aptos-mainnet"); c != 2 || out != "" {
			t.Fatalf("%s: %d %s %s", path, c, out, e)
		}
	}
	for _, args := range [][]string{
		{"rest", "GET", "/unknown", "-n", "sei-mainnet"},
		{"rest", "GET", "/unknown", "-n", "tron-mainnet"},
		{"rest", "GET", "/", "-n", "ethereum-mainnet"},
		{"rest", "GET", "/accounts/0x1", "-n", "aptos-mainnet", "--body", "{}"},
		{"rest", "POST", "/view", "-n", "aptos-mainnet", "--body", "{}", "--body-file", "request.json"},
		{"rest", "POST", "/view", "-n", "aptos-mainnet", "--body", "@request.json"},
		{"rest", "POST", "/view", "-n", "aptos-mainnet", "--body", "not-json"},
		{"rest", "POST", "/view", "-n", "aptos-mainnet", "--query", "invalid"},
		{"rpc", "status", "-n", "aptos-mainnet"},
		{"rpc", "ambiguous", "-n", "sei-mainnet"},
		{"rpc", "getHealth", "-n", "solana-testnet"},
	} {
		if c, out, e := run(t, a, args...); c != 2 || out != "" {
			t.Fatalf("%v: %d %s %s", args, c, out, e)
		}
	}
}

func TestEntityLookupExplicitNetworksAndRawResult(t *testing.T) {
	a := productTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "test-product-key"
		}
		if name == "NODIT_NETWORK" {
			return "invalid-default"
		}
		return ""
	}
	calls := 0
	mockAPI(a, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/multichain/lookupEntities" {
			t.Fatal(r.URL.Path)
		}
		assertBody(t, r, `{"input":" 0x1 ","chains":["aptos-mainnet","ethereum-mainnet"]}`)
		return response(200, `{"input":" 0x1 ","items":[{"chain":"aptos-mainnet","type":"ACCOUNT","normalizedInput":"0x0001"}]}`), nil
	})
	if c, out, e := run(t, a, "data", "entity", "lookup", " 0x1 ", "--networks", "aptos-mainnet,ethereum-mainnet", "-o", "json"); c != 0 || !strings.Contains(out, `"normalizedInput": "0x0001"`) {
		t.Fatalf("%d %s %s", c, out, e)
	}
	for _, networks := range []string{"ethereum-mainnet,unknown-network", "solana-devnet", "ethereum-mainnet,ethereum-mainnet", ""} {
		if c, out, e := run(t, a, "data", "entity", "lookup", "0x1", "--networks", networks); c != 2 || out != "" {
			t.Fatalf("%d %s %s", c, out, e)
		}
	}
	if c, _, e := run(t, a, "data", "entity", "lookup", "0x1", "--networks", "aptos-mainnet", "--network", "aptos-mainnet"); c != 2 || !strings.Contains(e, "--networks") {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("lookup made extra requests")
	}
}

func TestRESTNonJSONAndRedirect(t *testing.T) {
	for _, reply := range []*http.Response{response(200, "binary BCS"), response(302, `{}`)} {
		a := productTestApp(t)
		calls := 0
		reply.Header.Set("Location", "https://example.com/")
		mockAPI(a, func(*http.Request) (*http.Response, error) { calls++; return reply, nil })
		if c, out, e := run(t, a, "rest", "GET", "/", "-n", "aptos-mainnet"); c != 1 || out != "" || calls != 1 {
			t.Fatalf("%d %s %s", c, out, e)
		}
	}
}

func TestACredentialIsCheckedBeforeArgumentFormat(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(string) string { return "" }
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		t.Fatal("the request left the CLI")
		return nil, nil
	})
	// Without a key the address can never be made to work, so the key is what the caller hears about.
	for _, args := range [][]string{
		{"data", "native", "balance", "--address", "notanaddress", "-n", "ethereum-mainnet"},
		{"rpc", "eth_blockNumber", "-n", "bogus-network"},
	} {
		if c, out, e := run(t, a, args...); c != 1 || out != "" || !strings.Contains(e, "API_KEY_REQUIRED") {
			t.Fatalf("%v: %d %s %s", args, c, out, e)
		}
	}
}

func TestAptosVersionBeyondUint64IsRejectedLocally(t *testing.T) {
	// A key is present so the run reaches the argument checks; the missing-key case is its own test.
	a := productTestApp(t)
	mockAPI(a, func(*http.Request) (*http.Response, error) {
		t.Fatal("the request left the CLI")
		return nil, nil
	})
	c, out, e := run(t, a, "data", "transaction", "get", "--id", strings.Repeat("1", 64), "-n", "aptos-mainnet", "-o", "json")
	if c != 2 || out != "" || !strings.Contains(e, "too large for an Aptos version") {
		t.Fatalf("%d %s %s", c, out, e)
	}
}
