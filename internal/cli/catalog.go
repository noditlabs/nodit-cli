package cli

import (
	"slices"
	"strings"
)

type network struct {
	ID       string   `json:"id"`
	Chain    string   `json:"chain"`
	Network  string   `json:"network"`
	Products []string `json:"products"`
}

// The release snapshot describes public products, independently of account plans.
// Network names are endpoint identifiers, without a separate environment taxonomy.
var networks = makeCatalog()

func makeCatalog() []network {
	groups := []struct{ chain, names, products string }{
		{"aptos", "mainnet testnet", "node data webhook"},
		{"arbitrum", "mainnet sepolia", "node data webhook stream"},
		{"arc", "testnet", "node data webhook stream"},
		{"avalanche", "mainnet", "node data webhook stream"},
		{"avalanche", "fuji", "node"},
		{"babylon", "mainnet", "node"},
		{"base", "mainnet sepolia", "node data webhook stream"},
		{"bitcoin", "mainnet", "node data"},
		{"bitcoincash", "mainnet", "data"},
		{"bnb", "mainnet testnet", "node data webhook stream"},
		{"celestia", "mainnet", "node"},
		{"chiliz", "mainnet", "data"},
		{"cosmos", "mainnet", "node"},
		{"cronospos", "mainnet", "node"},
		{"dogecoin", "mainnet", "data"},
		{"ethereum", "mainnet sepolia hoodi", "node data webhook stream"},
		{"ethereumclassic", "mainnet", "data"},
		{"giwa", "sepolia", "node data webhook stream"},
		{"hippo", "mainnet", "node"},
		{"initia", "mainnet", "node"},
		{"injective", "mainnet", "node"},
		{"kaia", "mainnet kairos", "node data webhook stream"},
		{"luniverse", "mainnet", "data"},
		{"metal", "mainnet", "node"},
		{"optimism", "mainnet sepolia", "node data webhook stream"},
		{"polygon", "mainnet", "node data webhook stream"},
		{"sei", "mainnet", "node"},
		{"solana", "mainnet", "node"},
		{"solana", "devnet", "node"},
		{"sui", "mainnet", "node"},
		{"tron", "mainnet", "node data webhook stream"},
		{"worldchain", "mainnet", "node"},
		{"xrpl", "mainnet", "node data"},
	}
	result := make([]network, 0)
	for _, g := range groups {
		for _, name := range strings.Fields(g.names) {
			result = append(result, network{g.chain + "-" + name, g.chain, name, strings.Fields(g.products)})
		}
	}
	slices.SortFunc(result, func(a, b network) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func findNetwork(id string) (network, error) {
	for _, n := range networks {
		if n.ID == id {
			return n, nil
		}
	}
	return network{}, unsupportedNetwork(id)
}

// Catalog IDs close to what was typed. Naming the chain alone is the usual slip, so those come
// first, and the list stays short enough for an error message.
func nearbyNetworks(id string) []string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return nil
	}
	var byChain, byName []string
	for _, n := range networks {
		switch {
		case n.Chain == id:
			byChain = append(byChain, n.ID)
		case n.Network == id || strings.HasPrefix(n.ID, id):
			byName = append(byName, n.ID)
		}
	}
	near := append(byChain, byName...)
	if len(near) > 3 {
		near = near[:3]
	}
	return near
}

func filterNetworks(chain, product string) ([]network, error) {
	if product != "" && !slices.Contains([]string{"node", "data", "webhook", "stream"}, product) {
		return nil, invalid("Unknown product. Use node, data, webhook, or stream.")
	}
	result := make([]network, 0)
	knownChain := chain == ""
	for _, n := range networks {
		if chain != "" && n.Chain != chain {
			continue
		}
		knownChain = true
		if product == "" || slices.Contains(n.Products, product) {
			result = append(result, n)
		}
	}
	if !knownChain {
		return nil, invalid("Unsupported chain. Run nodit network list.")
	}
	return result, nil
}
