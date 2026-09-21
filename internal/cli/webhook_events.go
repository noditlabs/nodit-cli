package cli

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"
)

// Classic has no endpoint that serves these, unlike Flexible's stream schemas, so the catalog is
// carried in the binary the way the network catalog is. It is reference only: the server decides
// what it accepts, and the CLI does not validate an event type against this list.
//
// The networks are resolved from the catalog rather than named here, so the answer stays a list of
// ids that --network actually accepts and follows the catalog when webhook support changes.
type classicEvent struct {
	name, chains, summary string
	sets                  []conditionSet
	example               string
}

type conditionSet struct {
	name   string
	fields []conditionField
}

type conditionField struct {
	name, kind string
	required   bool
	note       string
}

var classicEvents = []classicEvent{
	{
		name: "ADDRESS_ACTIVITY", chains: evmDataChains + " tron",
		summary: "Activity of the listed accounts, as sender or recipient",
		sets: []conditionSet{{fields: []conditionField{
			{"addresses", "array of string", true, "Accounts to monitor"},
		}}},
		example: `{"eventType":"ADDRESS_ACTIVITY","description":"my hook","notification":{"webhookUrl":"https://example.com/hook"},"condition":{"addresses":["0x000000000000000000000000000000000000dEaD"]}}`,
	},
	{
		name: "TOKEN_TRANSFER", chains: evmDataChains + " tron",
		summary: "Transfers of the listed token contracts",
		sets: []conditionSet{{fields: []conditionField{
			{"tokens", "array of object", true, "Each entry requires contractAddress"},
		}}},
		example: `{"eventType":"TOKEN_TRANSFER","notification":{"webhookUrl":"https://example.com/hook"},"condition":{"tokens":[{"contractAddress":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}}`,
	},
	{
		name: "LOG", chains: evmDataChains + " tron",
		summary: "Event logs of one contract, filtered by topic",
		sets: []conditionSet{{fields: []conditionField{
			{"address", "string", true, "Contract emitting the log"},
			{"topics", "array of string", true, "Up to 4 topics; topics[0] is the event signature hash"},
		}}},
		example: `{"eventType":"LOG","notification":{"webhookUrl":"https://example.com/hook"},"condition":{"address":"0xdAC17F958D2ee523a2206206994597C13D831ec7","topics":["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"]}}`,
	},
	{
		name: "TRANSACTION", chains: "aptos",
		summary: "Aptos transactions matching a function or an event",
		sets: []conditionSet{
			{name: "function", fields: []conditionField{
				{"payloadFunction", "string", true, "Full function name, module_address::module_name::function_name"},
			}},
			{name: "event", fields: []conditionField{
				{"eventType", "string", true, "Full event type, module_address::module_name::event_name"},
				{"eventAccountAddress", "string", true, "Address emitting the event; 0x0 for module events"},
			}},
		},
		example: `{"eventType":"TRANSACTION","notification":{"webhookUrl":"https://example.com/hook"},"condition":{"payloadFunction":"0x1::aptos_account::transfer"}}`,
	},
	{
		name: "EVENT", chains: "aptos",
		summary: "Aptos events of one type",
		sets: []conditionSet{{fields: []conditionField{
			{"eventType", "string", true, "Full event type, module_address::module_name::event_name"},
			{"eventAccountAddress", "string", true, "Address emitting the event; 0x0 for module events"},
		}}},
		example: `{"eventType":"EVENT","notification":{"webhookUrl":"https://example.com/hook"},"condition":{"eventType":"0x1::coin::CoinDeposit","eventAccountAddress":"0x0"}}`,
	},
}

// The chains each event type serves, as network ids carrying the webhook product.
func (e classicEvent) networks() []string {
	ids := []string{}
	for _, n := range networks {
		if inWords(e.chains, n.Chain) && inWords(strings.Join(n.Products, " "), "webhook") {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func classicEventNames() string {
	names := make([]string, 0, len(classicEvents))
	for _, e := range classicEvents {
		names = append(names, e.name)
	}
	return strings.Join(names, ", ")
}

func (e classicEvent) describe() map[string]any {
	sets := make([]any, 0, len(e.sets))
	for _, set := range e.sets {
		fields := make([]any, 0, len(set.fields))
		for _, f := range set.fields {
			field := map[string]any{"name": f.name, "type": f.kind, "required": f.required}
			if f.note != "" {
				field["description"] = f.note
			}
			fields = append(fields, field)
		}
		described := map[string]any{"fields": fields}
		if set.name != "" {
			described["name"] = set.name
		}
		sets = append(sets, described)
	}
	described := map[string]any{
		"eventType":     e.name,
		"networks":      e.networks(),
		"description":   e.summary,
		"conditionSets": sets,
	}
	// Emitted as a value, not a quoted string, so it can be piped straight back into --body.
	var body any
	if json.Unmarshal([]byte(e.example), &body) == nil {
		described["exampleBody"] = body
	}
	return described
}

func (a *app) classicEventTypesCommand() *cobra.Command {
	return &cobra.Command{
		Use: "event-types", Short: "List Classic Webhook event types", Args: cobra.NoArgs,
		Long: "List the event types a Classic Webhook can subscribe to.\n" +
			"The list is carried in the binary and needs no credentials. It is reference for writing\n" +
			"--body; the server decides what it accepts.\n" +
			"See the networks, condition fields and a body example with\nnodit webhook classic schema <event-type>.",
		RunE: func(*cobra.Command, []string) error {
			items := make([]any, 0, len(classicEvents))
			for _, e := range classicEvents {
				items = append(items, map[string]any{
					"eventType":   e.name,
					"description": e.summary,
				})
			}
			return a.success(map[string]any{"eventTypes": items})
		},
	}
}

func (a *app) classicEventSchemaCommand() *cobra.Command {
	return &cobra.Command{
		Use: "schema <event-type>", Short: "Show a Classic Webhook event type's condition fields",
		Args: helpOnNoArgs(cobra.ExactArgs(1)),
		Long: "Show the condition fields a Classic Webhook event type takes, with a complete --body example.\n" +
			"The definition is carried in the binary and needs no credentials. It is reference only; the\n" +
			"server decides what it accepts.\nList the event types with nodit webhook classic event-types.",
		Example: "  nodit webhook classic schema ADDRESS_ACTIVITY",
		RunE: func(_ *cobra.Command, args []string) error {
			for _, e := range classicEvents {
				if strings.EqualFold(e.name, args[0]) {
					return a.success(e.describe())
				}
			}
			return invalid("Unknown event type " + args[0] + ". Known types are " + classicEventNames() + ".")
		},
	}
}
