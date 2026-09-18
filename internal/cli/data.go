package cli

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const evmDataChains = "arbitrum arc avalanche base bnb chiliz ethereum ethereumclassic giwa kaia luniverse optimism polygon"
const utxoChains = "bitcoin bitcoincash dogecoin"

type dataSpec struct {
	group, name, summary, operation, chains, flags, example string
}

// Routes are explicit public API contracts, not user-facing operation IDs.
var dataSpecs = []dataSpec{
	{"native", "balance", "Get an account's native balance", "native/getNativeBalanceByAccount", evmDataChains + " tron " + utxoChains + " xrpl", "address", "--address 0x000000000000000000000000000000000000dEaD"},
	{"token", "balances", "List an account's token balances", "token/getTokensOwnedByAccount", evmDataChains + " tron aptos", "address contract paging", "--address 0x000000000000000000000000000000000000dEaD --rpp 10"},
	{"token", "transfers", "List token transfers by account or contract", "token/getTokenTransfersByAccount", evmDataChains + " tron xrpl", "address contract paging range relation ledger", "--contract 0xdAC17F958D2ee523a2206206994597C13D831ec7 --rpp 10"},
	{"token", "metadata", "Get token contract metadata", "token/getTokenContractMetadataByContracts", evmDataChains + " tron", "contract", "--contract 0xdAC17F958D2ee523a2206206994597C13D831ec7"},
	{"token", "holders", "List token contract holders", "token/getTokenHoldersByContract", evmDataChains + " tron", "contract paging", "--contract 0xdAC17F958D2ee523a2206206994597C13D831ec7 --rpp 10"},
	{"nft", "list", "List an account's NFTs", "nft/getNftsOwnedByAccount", evmDataChains, "address contract paging", "--address 0x000000000000000000000000000000000000dEaD --rpp 10"},
	{"nft", "transfers", "List NFT transfers by account or contract", "nft/getNftTransfersByAccount", evmDataChains, "address contract paging range relation", "--address 0x000000000000000000000000000000000000dEaD --rpp 10"},
	{"transaction", "list", "List an account's transactions", "blockchain/getTransactionsByAccount", evmDataChains + " aptos tron " + utxoChains + " xrpl", "address paging range relation ledger", "--address 0x000000000000000000000000000000000000dEaD --rpp 10"},
	{"transaction", "get", "Get a transaction by hash, ID, or Aptos version", "blockchain/getTransactionByHash", evmDataChains + " aptos " + utxoChains + " xrpl", "id", "--id 0x4e3a375441017e5f68ad07615659d7ccbbafbbdd998a850dec3e544601cc0db9"},
	{"block", "get", "Get a block or XRPL ledger by number or hash", "blockchain/getBlockByHashOrNumber", evmDataChains + " aptos " + utxoChains + " xrpl", "number id", "--number latest"},
	{"event", "by-account", "List Aptos events emitted by an account", "blockchain/getEventsByAccount", "aptos", "address event-types paging range", "--address 0x1 --rpp 10"},
	{"event", "by-type", "List Aptos events by full Move event type", "blockchain/getEventsByType", "aptos", "event-types paging range", "--event-type 0x1::fungible_asset::Withdraw --rpp 10"},
}

type dataInput struct {
	address, contract, id, number, relation                    string
	fromBlock, toBlock, fromDate, toDate, fromLedger, toLedger string
	cursor                                                     string
	page, rpp                                                  int
	withCount                                                  bool
	eventTypes                                                 []string
}

func (a *app) dataCommand() *cobra.Command {
	root := &cobra.Command{Use: "data", Short: "Query Web3 Data API using an API key"}
	groups := map[string]*cobra.Command{}
	for _, spec := range dataSpecs {
		group := groups[spec.group]
		if group == nil {
			group = &cobra.Command{Use: spec.group, Short: "Query " + spec.group + " data"}
			groups[spec.group] = group
			root.AddCommand(group)
		}
		group.AddCommand(a.dataLeaf(spec))
	}
	entity := &cobra.Command{Use: "entity", Short: "Find accounts or transactions across networks"}
	entity.AddCommand(a.entityLookupCommand())
	root.AddCommand(entity)
	return root
}

// Only the rules that bind this command. The whole set on every leaf buries the one line that
// applies to what was typed.
func dataLong(spec dataSpec, ids []string) string {
	notes := []string{
		spec.summary + ". API key required.",
		"Supported networks: " + strings.Join(ids, ", ") + ".",
		"Returns one response unchanged, including pagination fields and original numeric types.",
	}
	add := func(when bool, note string) {
		if when {
			notes = append(notes, note)
		}
	}
	has := func(field string) bool { return inWords(spec.flags, field) }
	add(has("paging"), "Returns one page; --page and --cursor are mutually exclusive.")
	add(spec.name == "transfers", "Requires --address or --contract; combining them filters the account by that contract.")
	add(spec.group == "token" && spec.name == "balances", "Aptos accepts --address without --contract.")
	add(spec.name == "transfers" && has("ledger"), "XRPL accepts --address only.")
	add(has("range") && has("ledger"), "XRPL ranges use --from-ledger/--to-ledger. Other chains use --from-block/--to-block.")
	add(has("range"), "Block, ledger and date ranges cannot be combined with each other.")
	add(has("id") && spec.group == "transaction", "IDs are hashes, UTXO transaction IDs, or a decimal Aptos version.")
	add(spec.group == "block", "Requires exactly one of --number (decimal, earliest, latest) or --id (hash).")
	return strings.Join(notes, "\n")
}

func (a *app) dataLeaf(spec dataSpec) *cobra.Command {
	var flags productFlags
	var v dataInput
	ids := []string{}
	for _, n := range networks {
		if inWords(spec.chains, n.Chain) && inWords(strings.Join(n.Products, " "), "data") {
			ids = append(ids, n.ID)
		}
	}
	cmd := &cobra.Command{
		Use: spec.name, Short: spec.summary, Args: cobra.NoArgs,
		Long:    dataLong(spec, ids),
		Example: "  nodit data " + spec.group + " " + spec.name + " " + spec.example + " -n ethereum-mainnet",
		RunE: func(cmd *cobra.Command, _ []string) error {
			n, err := a.productNetwork(flags.network, "data")
			if err != nil {
				return err
			}
			if !inWords(spec.chains, n.Chain) {
				return unsupportedOperation("This Data API command is not supported on the selected network.")
			}
			operation, body, err := dataRequest(cmd, spec, n, v)
			if err != nil {
				return err
			}
			key, err := a.apiKey(flags.apiKey)
			if err != nil {
				return err
			}
			endpoint := fmt.Sprintf("https://web3.%s/v1/%s/%s/%s", a.env.Domain, n.Chain, n.Network, operation)
			result, err := a.apiPost(cmd.Context(), endpoint, key, body)
			if err != nil {
				return err
			}
			return a.success(result)
		},
	}
	flags.bind(cmd)
	for _, f := range []struct {
		name  string
		value *string
		help  string
		short string
	}{
		{"address", &v.address, "Account address in the chain's own format, such as 0xdAC1...ec7", "a"},
		{"contract", &v.contract, "Token or NFT contract address, not an Aptos asset type", "c"},
		{"id", &v.id, "Transaction hash, UTXO ID, Aptos version, or block hash", "i"},
		{"number", &v.number, "Block number or ledger index: 21000000, earliest, or latest", ""},
		{"relation", &v.relation, "Account transfer relation: from, to, or both", ""},
	} {
		if inWords(spec.flags, f.name) {
			cmd.Flags().StringVarP(f.value, f.name, f.short, "", f.help)
		}
	}
	if inWords(spec.flags, "paging") {
		cmd.Flags().IntVar(&v.page, "page", 0, "Page number, 1-100 (mutually exclusive with --cursor)")
		cmd.Flags().IntVar(&v.rpp, "rpp", 0, "Results per page, 1-1000 (API default when omitted)")
		cmd.Flags().StringVar(&v.cursor, "cursor", "", "Cursor value copied from the previous response")
		cmd.Flags().BoolVar(&v.withCount, "with-count", false, "Request total count; may make the query slower")
	}
	if inWords(spec.flags, "event-types") {
		cmd.Flags().StringSliceVar(&v.eventTypes, "event-type", nil, "Full Move event type, such as 0x1::coin::Deposit (repeatable)")
	}
	if inWords(spec.flags, "range") {
		cmd.Flags().StringVar(&v.fromBlock, "from-block", "", "First block: decimal, hash, earliest, or latest")
		cmd.Flags().StringVar(&v.toBlock, "to-block", "", "Last block: decimal, hash, earliest, or latest")
		cmd.Flags().StringVar(&v.fromDate, "from-date", "", "Start time with a timezone, such as 2026-09-01T00:00:00Z")
		cmd.Flags().StringVar(&v.toDate, "to-date", "", "End time with a timezone, such as 2026-09-18T00:00:00Z")
	}
	if inWords(spec.flags, "ledger") {
		cmd.Flags().StringVar(&v.fromLedger, "from-ledger", "", "First XRPL ledger: decimal, hash, earliest, or latest")
		cmd.Flags().StringVar(&v.toLedger, "to-ledger", "", "Last XRPL ledger: decimal, hash, earliest, or latest")
	}
	return cmd
}

func dataRequest(cmd *cobra.Command, spec dataSpec, n network, v dataInput) (string, map[string]any, error) {
	body := map[string]any{}
	if err := validateChangedDataInputs(cmd); err != nil {
		return "", nil, err
	}
	if err := addDataAddresses(spec, n, v, body); err != nil {
		return "", nil, err
	}
	op, err := selectDataOperation(spec, n, v, body)
	if err != nil {
		return "", nil, err
	}
	if err = addEventTypes(spec, v.eventTypes, body); err != nil {
		return "", nil, err
	}
	if err = addTransferRelation(v, body); err != nil {
		return "", nil, err
	}
	if err = dataPagination(cmd, v, body); err != nil {
		return "", nil, err
	}
	if err = dataRange(n, v, body); err != nil {
		return "", nil, err
	}
	return op, body, nil
}

func validateChangedDataInputs(cmd *cobra.Command) error {
	for _, f := range []string{"address", "contract", "id", "number", "cursor", "relation", "from-block", "to-block", "from-ledger", "to-ledger", "from-date", "to-date"} {
		if cmd.Flags().Changed(f) {
			value, _ := cmd.Flags().GetString(f)
			if strings.TrimSpace(value) == "" {
				return invalid("--" + f + " cannot be empty.")
			}
		}
	}
	return nil
}

func addDataAddresses(spec dataSpec, n network, v dataInput, body map[string]any) error {
	if v.address != "" {
		if err := dataAddress(n, v.address); err != nil {
			return err
		}
		body["accountAddress"] = v.address
	}
	if v.contract != "" {
		if n.Chain == "aptos" || n.Chain == "xrpl" {
			return unsupportedOperation("--contract is not supported for this chain's asset model.")
		}
		if err := dataAddress(n, v.contract); err != nil {
			return err
		}
		if v.address != "" || spec.name == "metadata" {
			body["contractAddresses"] = []string{v.contract}
		} else {
			body["contractAddress"] = v.contract
		}
	}
	return nil
}

func selectDataOperation(spec dataSpec, n network, v dataInput, body map[string]any) (string, error) {
	op := spec.operation
	switch spec.group + " " + spec.name {
	case "native balance", "token balances", "nft list", "transaction list":
		if v.address == "" {
			return "", invalid("--address is required.")
		}
		if spec.group == "native" && inWords(utxoChains+" xrpl", n.Chain) {
			op = "native/getNativeTokenBalanceByAccount"
		}
	case "token metadata", "token holders":
		if v.contract == "" {
			return "", invalid("--contract is required.")
		}
	case "token transfers", "nft transfers":
		if v.address == "" && v.contract == "" {
			return "", invalid("Specify --address, --contract, or both to filter an account by contract.")
		}
		if v.address == "" {
			op = strings.Replace(op, "ByAccount", "ByContract", 1)
		}
	case "transaction get":
		field := "transactionHash"
		var id any = v.id
		if n.Chain == "aptos" && decimal(v.id) {
			op, field = "blockchain/getTransactionByVersion", "transactionVersion"
			number, _ := new(big.Int).SetString(v.id, 10)
			// A ledger version is a u64; a longer run of digits is a hash typed without its 0x.
			if !number.IsUint64() {
				return "", invalid("--id is too large for an Aptos version; a 32-byte hash needs the 0x prefix.")
			}
			id = json.Number(number.String())
		} else if inWords(utxoChains, n.Chain) {
			op, field = "blockchain/getTransactionByTransactionId", "transactionId"
			if !validHex(v.id, 32, false) {
				return "", invalid("--id must be a 64-digit transaction ID without 0x.")
			}
		} else if !validHex(v.id, 32, n.Chain != "xrpl") {
			return "", invalid("--id must be a 32-byte transaction hash (0x-prefixed except on XRPL), or a decimal Aptos version.")
		}
		body[field] = id
	case "block get":
		if (v.id == "") == (v.number == "") {
			return "", invalid("Specify exactly one of --number or --id.")
		}
		value := v.number
		if v.id != "" {
			if !validHex(v.id, 32, !inWords(utxoChains+" xrpl", n.Chain)) {
				return "", invalid("--id must be a 32-byte block or ledger hash in the selected chain's format.")
			}
			value = v.id
		} else if !decimal(value) && !inWords("earliest latest", value) {
			return "", invalid("--number must be decimal, earliest, or latest.")
		}
		field := "block"
		if n.Chain == "xrpl" {
			op, field = "blockchain/getLedgerByHashOrIndex", "ledger"
		}
		body[field] = value
	case "event by-account":
		if v.address == "" {
			return "", invalid("--address is required.")
		}
	case "event by-type":
		if len(v.eventTypes) != 1 {
			return "", invalid("Exactly one --event-type is required.")
		}
	}
	return op, nil
}

func addEventTypes(spec dataSpec, eventTypes []string, body map[string]any) error {
	if len(eventTypes) == 0 {
		return nil
	}
	for _, eventType := range eventTypes {
		if strings.TrimSpace(eventType) == "" || strings.IndexFunc(eventType, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
			return invalid("Event types cannot be empty or contain whitespace.")
		}
	}
	if spec.name == "by-type" {
		body["eventType"] = eventTypes[0]
	} else {
		body["eventTypes"] = eventTypes
	}
	return nil
}

func addTransferRelation(v dataInput, body map[string]any) error {
	if v.relation != "" {
		if v.address == "" || !inWords("from to both", v.relation) {
			return invalid("--relation requires --address and must be from, to, or both.")
		}
		body["relation"] = v.relation
	}
	return nil
}

func dataAddress(n network, address string) error {
	if inWords(evmDataChains, n.Chain) {
		if !validHex(address, 20, true) {
			return invalid("Address must be a 0x-prefixed 20-byte EVM address.")
		}
	} else if strings.IndexFunc(address, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
		return invalid("Address must use the selected chain's format without whitespace.")
	}
	return nil
}

func dataPagination(cmd *cobra.Command, v dataInput, body map[string]any) error {
	if cmd.Flags().Changed("page") {
		if v.page < 1 || v.page > 100 || cmd.Flags().Changed("cursor") {
			return invalid("--page must be 1-100 and cannot be combined with --cursor.")
		}
		body["page"] = v.page
	}
	if cmd.Flags().Changed("rpp") {
		if v.rpp < 1 || v.rpp > 1000 {
			return invalid("--rpp must be 1-1000.")
		}
		body["rpp"] = v.rpp
	}
	if cmd.Flags().Changed("cursor") {
		body["cursor"] = v.cursor
	}
	if cmd.Flags().Changed("with-count") {
		body["withCount"] = v.withCount
	}
	return nil
}

func dataRange(n network, v dataInput, body map[string]any) error {
	from, to, field, err := selectChainRange(n, v)
	if err != nil {
		return err
	}
	if (from != "" || to != "") && (v.fromDate != "" || v.toDate != "") {
		return invalid("Block or ledger ranges cannot be combined with date ranges.")
	}
	if err = validateChainRange(n, from, to); err != nil {
		return err
	}
	if from != "" {
		body["from"+field] = from
	}
	if to != "" {
		body["to"+field] = to
	}
	return addDateRange(v.fromDate, v.toDate, body)
}

func selectChainRange(n network, v dataInput) (string, string, string, error) {
	from, to, field := v.fromBlock, v.toBlock, "Block"
	if n.Chain == "xrpl" {
		if from != "" || to != "" {
			return "", "", "", invalid("XRPL uses --from-ledger and --to-ledger instead of block ranges.")
		}
		from, to, field = v.fromLedger, v.toLedger, "Ledger"
	} else if v.fromLedger != "" || v.toLedger != "" {
		return "", "", "", invalid("Ledger ranges are only supported on XRPL.")
	}
	return from, to, field, nil
}

func validateChainRange(n network, from, to string) error {
	for _, value := range []string{from, to} {
		if value != "" && !decimal(value) && !inWords("earliest latest", value) && !validHex(value, 32, !inWords(utxoChains+" tron xrpl", n.Chain)) {
			return invalid("Range bounds must be decimal, earliest, latest, or a hash in the selected chain's format.")
		}
	}
	if (from == "latest" && to != "latest") || (to == "earliest" && from != "earliest") {
		return invalid("A latest start requires a latest end; an earliest end requires an earliest start.")
	}
	if decimal(from) && decimal(to) {
		first, _ := new(big.Int).SetString(from, 10)
		last, _ := new(big.Int).SetString(to, 10)
		if first.Cmp(last) > 0 {
			return invalid("The range start cannot exceed the end.")
		}
	}
	return nil
}

func addDateRange(from, to string, body map[string]any) error {
	var dates [2]time.Time
	for i, value := range []string{from, to} {
		if value == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return invalid("Dates need a timezone, such as 2026-09-01T00:00:00Z.")
		}
		dates[i] = t
	}
	if !dates[0].IsZero() && !dates[1].IsZero() && dates[0].After(dates[1]) {
		return invalid("--from-date cannot be later than --to-date.")
	}
	if from != "" {
		body["fromDate"] = from
	}
	if to != "" {
		body["toDate"] = to
	}
	return nil
}
