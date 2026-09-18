package cli

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

type productFlags struct{ network, apiKey string }

func (f *productFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.network, "network", "n", "", "Network ID (then NODIT_NETWORK, then config.network)")
	_ = cmd.RegisterFlagCompletionFunc("network", completeNetwork)
	f.bindKey(cmd)
}

func (f *productFlags) bindKey(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.apiKey, "api-key", "", "API key override (prefer NODIT_API_KEY to keep it out of shell history)")
}

func (a *app) productNetwork(explicit, product string) (network, error) {
	var c config
	if explicit == "" && a.getenv("NODIT_NETWORK") == "" {
		var err error
		c, err = a.config.read()
		if err != nil {
			return network{}, err
		}
	}
	id, err := resolveNetwork(explicit, a.getenv("NODIT_NETWORK"), c)
	if err != nil {
		return network{}, err
	}
	n, err := findNetwork(id)
	if err != nil {
		return network{}, err
	}
	if !slices.Contains(n.Products, product) {
		return network{}, unsupportedOperation("This network does not support the requested product.")
	}
	return n, nil
}

func unsupportedOperation(message string) error {
	e := failure("UNSUPPORTED_OPERATION", message)
	e.exit = 2
	return e
}

func inWords(words, value string) bool { return slices.Contains(strings.Fields(words), value) }

func validHex(value string, size int, prefix bool) bool {
	if prefix {
		if !strings.HasPrefix(value, "0x") && !strings.HasPrefix(value, "0X") {
			return false
		}
		value = value[2:]
	}
	if len(value) != size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decimal(value string) bool {
	return value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func readJSONBody(value string) (json.RawMessage, error) {
	content := []byte(value)
	if strings.HasPrefix(value, "@") {
		f, err := os.Open(strings.TrimPrefix(value, "@"))
		if err != nil {
			return nil, invalid("Cannot open the JSON body file.")
		}
		defer f.Close()
		content, err = io.ReadAll(io.LimitReader(f, maxAPIResponseBytes+1))
		if err != nil {
			return nil, invalid("Cannot read the JSON body file.")
		}
	}
	if len(content) > maxAPIResponseBytes {
		return nil, invalid("JSON body exceeds the 16 MiB limit.")
	}
	if !json.Valid(content) {
		return nil, invalid("Body must be valid JSON or @ followed by a JSON file path.")
	}
	return json.RawMessage(content), nil
}

func readJSONFile(path string) (json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, invalid("Cannot open the JSON input file.")
	}
	defer f.Close()
	raw, err := readJSONReader(f)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, invalid("Input must be valid JSON.")
	}
	return raw, nil
}

func readJSONReader(r io.Reader) (json.RawMessage, error) {
	content, err := io.ReadAll(io.LimitReader(r, maxAPIResponseBytes+1))
	if err != nil {
		return nil, invalid("Cannot read JSON input.")
	}
	if len(content) > maxAPIResponseBytes {
		return nil, invalid("JSON input exceeds the 16 MiB limit.")
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return nil, nil
	}
	if !json.Valid(content) {
		return nil, invalid("Input must be valid JSON.")
	}
	return json.RawMessage(content), nil
}

func readInlineJSON(value string) (json.RawMessage, error) {
	raw, err := readJSONReader(strings.NewReader(value))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, invalid("Input must be valid JSON.")
	}
	return raw, nil
}
