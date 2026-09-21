package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// The lookup API accepts its own set of networks, which is not the set the Node and Data products serve.
// aptos-testnet and arc-testnet answer on stg and prd even though the published enum leaves them out.
const lookupNetworks = "aptos-mainnet aptos-testnet arbitrum-mainnet arbitrum-sepolia arc-testnet base-mainnet base-sepolia bitcoin-mainnet bitcoincash-mainnet bnb-mainnet bnb-testnet chiliz-mainnet dogecoin-mainnet ethereum-mainnet ethereum-sepolia ethereum-hoodi ethereumclassic-mainnet giwa-sepolia kaia-mainnet kaia-kairos optimism-mainnet optimism-sepolia polygon-mainnet luniverse-mainnet tron-mainnet xrpl-mainnet"

func (a *app) entityLookupCommand() *cobra.Command {
	var flags productFlags
	var ids []string
	cmd := &cobra.Command{
		Use: "lookup <input>", Short: "Identify accounts and transactions on explicit networks", Args: helpOnNoArgs(cobra.ExactArgs(1)),
		Long:    "Identify an input across multiple networks. --networks is required.\nThe input and the API's input/items/normalizedInput fields are preserved.\nThis command ignores NODIT_NETWORK and config.network; --network is not accepted.\nSupported networks: " + strings.Join(strings.Fields(lookupNetworks), ", ") + ".",
		Example: "  nodit data entity lookup 0x000000000000000000000000000000000000dEaD --networks ethereum-mainnet,base-mainnet",
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := a.apiKey(flags.apiKey)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("network") {
				return invalid("Entity lookup requires --networks, not --network.")
			}
			if len(ids) == 0 || strings.TrimSpace(args[0]) == "" {
				return invalid("Provide a non-empty input and --networks <id,id,...>.")
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if _, err := findNetwork(id); err != nil {
					return err
				}
				if !inWords(lookupNetworks, id) {
					return unsupportedOperation("Entity lookup is not supported on network " + id + ".")
				}
				if seen[id] {
					return invalid("--networks cannot contain duplicate IDs.")
				}
				seen[id] = true
			}
			result, err := a.apiPost(cmd.Context(), "https://web3."+a.env.Domain+"/v1/multichain/lookupEntities", key, map[string]any{"input": args[0], "chains": ids})
			if err != nil {
				return err
			}
			return a.success(result)
		},
	}
	flags.bind(cmd)
	// --network comes with the shared product flags but is rejected here, so it is kept out of the
	// help and the completions rather than offered as something this command takes.
	_ = cmd.Flags().MarkHidden("network")
	cmd.Flags().StringSliceVar(&ids, "networks", nil, "Required comma-separated network IDs; ignores the default network")
	return cmd
}
