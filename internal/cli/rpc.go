package cli

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) rpcCommand() *cobra.Command {
	var flags productFlags
	var params, paramsFile string
	cmd := &cobra.Command{
		Use: "rpc <method> [params...]", Short: "Call Node JSON-RPC using an API key", Args: cobra.MinimumNArgs(1),
		Long:    "Call JSON-RPC on catalog EVM, Solana, Sui, Cosmos, Bitcoin, Tron, and XRPL networks.\nXRPL answers outside JSON-RPC 2.0 and its errors arrive with HTTP 200.\nAptos uses nodit rest. Sei and Injective route known CometBFT methods to rpc- hosts,\nand eth_, net_, web3_, debug_, and trace_ methods to evm- hosts.\nPass simple params after the method. Valid JSON values keep their types; other values become strings.\nUse --params or --params-file for a complete JSON array. Piped input is used when no params are given.\nThe original JSON-RPC response is preserved. No unit conversion or automatic retry is performed.\nMethods can change blockchain state.",
		Example: "  nodit rpc eth_getBalance 0x000000000000000000000000000000000000dEaD latest -n ethereum-mainnet",
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := a.productNetwork(flags.network, "node")
			if err != nil {
				return err
			}
			method := args[0]
			if method == "" || strings.IndexFunc(method, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
				return invalid("RPC method must be a non-empty name without whitespace.")
			}
			rawParams, err := a.rpcParams(cmd, args[1:], params, paramsFile)
			if err != nil {
				return err
			}
			endpoint, err := a.rpcEndpoint(n, method)
			if err != nil {
				return err
			}
			key, err := a.apiKey(flags.apiKey)
			if err != nil {
				return err
			}
			body := struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      int             `json:"id"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params"`
			}{"2.0", 1, method, rawParams}
			result, err := a.apiPost(cmd.Context(), endpoint, key, body)
			if err != nil {
				return err
			}
			if n.Chain == "xrpl" {
				return a.xrplResponse(result)
			}
			rpc, ok := result.(map[string]any)
			if !ok || rpc["jsonrpc"] != "2.0" || rpc["id"] != json.Number("1") {
				return invalidRPCResponse()
			}
			_, hasResult := rpc["result"]
			if rawError := rpc["error"]; rawError != nil {
				problem, ok := rawError.(map[string]any)
				if !ok || hasResult {
					return invalidRPCResponse()
				}
				code, codeOK := problem["code"].(json.Number)
				message, messageOK := problem["message"].(string)
				if !codeOK || !messageOK {
					return invalidRPCResponse()
				}
				e := failure("RPC_ERROR", message)
				e.HTTPStatus, e.APICode, e.Details = 200, code, problem["data"]
				return e
			}
			if !hasResult {
				return invalidRPCResponse()
			}
			return a.success(result)
		},
	}
	cmd.Flags().StringVar(&params, "params", "", "Complete JSON array of RPC parameters")
	cmd.Flags().StringVar(&paramsFile, "params-file", "", "Path to a JSON array of RPC parameters")
	flags.bind(cmd)
	return cmd
}

func (a *app) rpcParams(cmd *cobra.Command, values []string, inline, path string) (json.RawMessage, error) {
	sources := 0
	if len(values) > 0 {
		sources++
	}
	if cmd.Flags().Changed("params") {
		sources++
	}
	if cmd.Flags().Changed("params-file") {
		sources++
	}
	if sources > 1 {
		return nil, invalid("Use positional params, --params, or --params-file, not more than one.")
	}
	var raw json.RawMessage
	var err error
	switch {
	case len(values) > 0:
		raw, err = positionalRPCParams(values)
	case cmd.Flags().Changed("params"):
		raw = json.RawMessage(strings.TrimSpace(inline))
	case cmd.Flags().Changed("params-file"):
		raw, err = readJSONFile(path)
	case a.stdinRedirected:
		raw, err = readJSONReader(a.stdin)
	default:
		raw = json.RawMessage("[]")
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		if sources > 0 {
			return nil, invalid("RPC params must be a valid JSON array.")
		}
		raw = json.RawMessage("[]")
	} else {
		raw = json.RawMessage(strings.TrimSpace(string(raw)))
	}
	if !json.Valid(raw) || raw[0] != '[' {
		return nil, invalid("RPC params must be a valid JSON array.")
	}
	return raw, nil
}

func positionalRPCParams(values []string) (json.RawMessage, error) {
	params := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if json.Valid([]byte(trimmed)) {
			params = append(params, json.RawMessage(trimmed))
			continue
		}
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return nil, invalid("RPC parameter contains invalid JSON.")
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, invalid("RPC parameter cannot be encoded as JSON.")
		}
		params = append(params, encoded)
	}
	return json.Marshal(params)
}

func invalidRPCResponse() error {
	return failure("INVALID_API_RESPONSE", "Node API returned an invalid JSON-RPC response.")
}

// XRPL answers outside JSON-RPC 2.0: success and application errors both arrive as
// HTTP 200 with a bare result object carrying status, and only an unknown method
// produces a 2.0 error envelope. Neither response echoes the request id.
func (a *app) xrplResponse(result any) error {
	rpc, ok := result.(map[string]any)
	if !ok {
		return invalidRPCResponse()
	}
	if rawError, present := rpc["error"]; present {
		problem, ok := rawError.(map[string]any)
		if !ok {
			return invalidRPCResponse()
		}
		code, codeOK := problem["code"].(json.Number)
		message, messageOK := problem["message"].(string)
		if !codeOK || !messageOK {
			return invalidRPCResponse()
		}
		e := failure("RPC_ERROR", message)
		e.HTTPStatus, e.APICode, e.Details = 200, code, problem["data"]
		return e
	}
	payload, ok := rpc["result"].(map[string]any)
	if !ok {
		return invalidRPCResponse()
	}
	if payload["status"] == "error" {
		message, ok := payload["error_message"].(string)
		if !ok {
			// Some errors carry only the error name, so it stands in as the message.
			if message, ok = payload["error"].(string); !ok {
				return invalidRPCResponse()
			}
		}
		e := failure("RPC_ERROR", message)
		e.HTTPStatus, e.APICode, e.Details = 200, payload["error_code"], payload
		return e
	}
	return a.success(result)
}
