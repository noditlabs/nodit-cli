package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

const cosmosChains = "babylon celestia cosmos cronospos hippo initia injective sei"
const cometMethods = "abci_info abci_query block block_by_hash block_results block_search blockchain broadcast_tx_async broadcast_tx_sync check_tx commit consensus_params genesis genesis_chunked header header_by_hash health net_info num_unconfirmed_txs status tx tx_search unconfirmed_txs validators"

func (a *app) rpcEndpoint(n network, method string) (string, error) {
	host := n.ID
	if n.Chain == "aptos" {
		return "", unsupportedOperation("Aptos Node API uses REST. Run nodit rest --help.")
	}
	if inWords(cosmosChains, n.Chain) {
		host = "rpc-" + n.ID
		if inWords("sei injective", n.Chain) && !inWords(cometMethods, method) {
			prefix, _, found := strings.Cut(method, "_")
			if !found || !inWords("eth net web3 debug trace", prefix) {
				return "", unsupportedOperation("Cannot determine the RPC interface for this method on Sei or Injective.")
			}
			host = "evm-" + n.ID
		}
	}
	return "https://" + host + "." + a.env.Domain + "/", nil
}

func (a *app) restEndpoint(n network, path string) (*url.URL, error) {
	u, err := parseRESTPath(path)
	if err != nil {
		return nil, err
	}
	host, err := routeRESTPath(n, u)
	if err != nil {
		return nil, err
	}
	u.Scheme, u.Host = "https", host+"."+a.env.Domain
	return u, nil
}

func parseRESTPath(path string) (*url.URL, error) {
	// Decode once for routing; reject encoded separators and path traversal instead
	// of allowing a proxy to interpret a different route than the CLI validated.
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || strings.ContainsAny(path, "?#\\") || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, invalid("REST path must start with one slash and contain no host, query string, or fragment. Use --query for query parameters.")
	}
	if strings.ContainsAny(u.Path, "\\?#") || strings.IndexFunc(u.Path, func(r rune) bool { return r <= ' ' || r == 127 }) >= 0 {
		return nil, invalid("REST path contains an invalid character.")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "%") {
			return nil, invalid("REST path cannot contain traversal or nested escapes.")
		}
	}
	if strings.Contains(strings.ToLower(path), "%2f") || strings.Contains(strings.ToLower(path), "%5c") {
		return nil, invalid("REST path cannot contain encoded separators.")
	}
	return u, nil
}

func routeRESTPath(n network, u *url.URL) (string, error) {
	host := n.ID
	switch {
	case n.Chain == "aptos":
		if u.Path == "/graphql" || strings.HasPrefix(u.Path, "/graphql/") || u.Path == "/v1" || strings.HasPrefix(u.Path, "/v1/") {
			return "", invalid("Use an Aptos REST path relative to /v1, such as /accounts/0x1. GraphQL is not a REST interface.")
		}
		u.Path = "/v1" + strings.TrimSuffix(u.Path, "/")
		u.RawPath = ""
	case n.Chain == "tron":
		if !strings.HasPrefix(u.Path, "/wallet/") && !strings.HasPrefix(u.Path, "/walletsolidity/") {
			return "", unsupportedOperation("Tron REST paths start with /wallet/ or /walletsolidity/.")
		}
	case inWords(cosmosChains, n.Chain):
		if inWords(cometMethods, strings.TrimPrefix(u.Path, "/")) {
			host = "rpc-" + n.ID
		} else if strings.HasPrefix(u.Path, "/cosmos/") || (n.Chain == "initia" && strings.HasPrefix(u.Path, "/initia/")) {
			host = "rest-" + n.ID
		} else {
			return "", unsupportedOperation("REST path does not identify a supported Cosmos SDK or CometBFT route.")
		}
	default:
		return "", unsupportedOperation("Node REST supports Aptos, Cosmos, and Tron networks. This network uses nodit rpc.")
	}
	return host, nil
}

func (a *app) restCommand() *cobra.Command {
	var flags productFlags
	var queries []string
	var body, bodyFile string
	cmd := &cobra.Command{
		Use: "rest <method> <path>", Short: "Call Aptos, Cosmos SDK, CometBFT, or Tron Node REST", Args: helpOnNoArgs(cobra.ExactArgs(2)),
		Long:    "Call a Node REST endpoint using an API key. Supports catalog Aptos, Cosmos, and Tron networks.\nAptos paths are relative to /v1; Cosmos SDK paths start with /cosmos/ (Initia also /initia/);\nCometBFT paths include /status, /block, /tx and other documented methods;\nTron paths start with /wallet/ or /walletsolidity/.\nUse repeated --query key=value options, --body for inline JSON, or --body-file for a JSON file.\nWhen no body option is given, piped input is used. GET bodies are rejected.\nSuccess includes the original JSON body and safe ledger, cursor, and rate limit headers.\nBinary BCS is unsupported. Requests never follow redirects or retry automatically.\nSome GET routes, including CometBFT broadcasts, can change blockchain state.",
		Example: "  nodit rest GET /accounts/0x1 -n aptos-mainnet\n  nodit rest GET /block -n cosmos-mainnet --query height=100\n  nodit rest POST /view -n aptos-mainnet --body-file request.json\n  nodit rest POST /wallet/getnowblock -n tron-mainnet --body '{}'",
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := a.apiKey(flags.apiKey)
			if err != nil {
				return err
			}
			n, err := a.productNetwork(flags.network, "node")
			if err != nil {
				return err
			}
			method := strings.ToUpper(args[0])
			if !inWords("GET POST PUT PATCH DELETE", method) {
				return invalid("REST method must be GET, POST, PUT, PATCH, or DELETE.")
			}
			endpoint, err := a.restEndpoint(n, args[1])
			if err != nil {
				return err
			}
			query := url.Values{}
			for _, entry := range queries {
				key, value, found := strings.Cut(entry, "=")
				if !found || key == "" || strings.TrimSpace(key) != key {
					return invalid("Each --query must be key=value with a non-empty key.")
				}
				query.Add(key, value)
			}
			endpoint.RawQuery = query.Encode()
			bodySet := cmd.Flags().Changed("body")
			bodyFileSet := cmd.Flags().Changed("body-file")
			if bodySet && bodyFileSet {
				return invalid("Use --body or --body-file, not both.")
			}
			if method == http.MethodGet && (bodySet || bodyFileSet) {
				return invalid("GET requests cannot contain a body.")
			}
			var requestBody any
			if bodySet || bodyFileSet || a.stdinRedirected {
				var raw []byte
				if bodySet {
					raw, err = readInlineJSON(body)
				} else if bodyFileSet {
					raw, err = readJSONFile(bodyFile)
				} else {
					raw, err = readJSONReader(a.stdin)
				}
				if err != nil {
					return err
				}
				if len(raw) > 0 {
					if method == http.MethodGet {
						return invalid("GET requests cannot contain a body.")
					}
					requestBody = json.RawMessage(raw)
				}
			}
			result, headers, err := a.apiRequest(cmd.Context(), method, endpoint.String(), key, requestBody)
			if err != nil {
				return err
			}
			return a.success(map[string]any{"body": result, "headers": headers})
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringArrayVar(&queries, "query", nil, "Query parameter, such as height=100 (repeatable, values are URL-encoded)")
	cmd.Flags().StringVar(&body, "body", "", "Inline JSON request body")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Path to a JSON request body")
	return cmd
}
