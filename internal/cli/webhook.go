package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type webhookFlags struct {
	productFlags
	body string
	yes  bool
}

type webhookKind string

const (
	classicWebhook  webhookKind = "webhooks"
	flexibleWebhook webhookKind = "flexible-webhooks"
)

func (k webhookKind) isFlexible() bool { return k == flexibleWebhook }

func (a *app) webhookCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "webhook", Short: "Manage Classic and Flexible Webhooks using an API key"})
	root.AddCommand(a.classicWebhookCommand(), a.flexibleWebhookCommand())
	return root
}

func (a *app) classicWebhookCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "classic", Short: "Manage Classic Webhooks"})
	root.AddCommand(a.webhookListCommand(classicWebhook), a.webhookGetCommand(classicWebhook), a.webhookBodyCommand(classicWebhook, "create"), a.webhookBodyCommand(classicWebhook, "update"), a.webhookDeleteCommand(classicWebhook), a.classicHistoryCommand(), a.classicAddressesCommand(), a.classicEventTypesCommand(), a.classicEventSchemaCommand())
	return root
}

func (a *app) flexibleWebhookCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "flexible", Short: "Manage Flexible Webhooks"})
	root.AddCommand(a.webhookListCommand(flexibleWebhook), a.webhookGetCommand(flexibleWebhook), a.webhookBodyCommand(flexibleWebhook, "create"), a.webhookBodyCommand(flexibleWebhook, "update"), a.webhookDeleteCommand(flexibleWebhook), a.flexibleStreamsCommand(), a.flexibleSchemaCommand())
	return root
}

func (a *app) webhookEndpoint(n network, kind webhookKind, suffix string) string {
	return fmt.Sprintf("https://web3.%s/v1/%s/%s/%s%s", a.env.Domain, n.Chain, n.Network, kind, suffix)
}

func (a *app) webhookListCommand(kind webhookKind) *cobra.Command {
	var flags productFlags
	page, rpp := 1, 10
	cmd := &cobra.Command{Use: "list", Short: "List webhooks on one network", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		if page < 1 || rpp < 1 || rpp > 100 {
			return invalid("--page must be positive and --rpp must be 1-100.")
		}
		u, _ := url.Parse(a.webhookEndpoint(n, kind, ""))
		q := u.Query()
		q.Set("page", fmt.Sprint(page))
		q.Set("rpp", fmt.Sprint(rpp))
		u.RawQuery = q.Encode()
		result, _, err := a.apiRequest(cmd.Context(), http.MethodGet, u.String(), key, nil)
		if err != nil {
			return err
		}
		if kind.isFlexible() {
			removeResponseField(result, "signingKey")
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	cmd.Flags().IntVar(&page, "page", 1, "Page number, starting at 1")
	cmd.Flags().IntVar(&rpp, "rpp", 10, "Results per page, 1-100")
	return cmd
}

func (a *app) webhookGetCommand(kind webhookKind) *cobra.Command {
	var flags productFlags
	cmd := &cobra.Command{Use: "get <id>", Short: "Get one webhook", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		if err := validID(args[0]); err != nil {
			return err
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		endpoint := a.webhookEndpoint(n, kind, "/"+args[0])
		if !kind.isFlexible() {
			u, _ := url.Parse(a.webhookEndpoint(n, classicWebhook, ""))
			q := u.Query()
			q.Set("subscriptionId", args[0])
			u.RawQuery = q.Encode()
			endpoint = u.String()
		}
		result, _, err := a.apiRequest(cmd.Context(), http.MethodGet, endpoint, key, nil)
		if err != nil {
			return err
		}
		if kind.isFlexible() {
			removeResponseField(result, "signingKey")
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	return cmd
}

func (a *app) webhookBodyCommand(kind webhookKind, action string) *cobra.Command {
	var flags webhookFlags
	use, method, short := action, http.MethodPost, "Create a webhook from JSON"
	// create takes its input entirely from --body, so only update has an argument to help about.
	args := cobra.PositionalArgs(cobra.NoArgs)
	if action == "update" {
		use, method, short = "update <id>", http.MethodPatch, "Update a webhook from JSON"
		args = helpOnNoArgs(cobra.ExactArgs(1))
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("network") {
			return invalid("Create and update require an explicit --network.")
		}
		suffix := ""
		if action == "update" {
			if err := validID(args[0]); err != nil {
				return err
			}
			suffix = "/" + args[0]
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		body, err := webhookBody(flags.body, kind, action)
		if err != nil {
			return err
		}
		result, _, err := a.apiRequest(cmd.Context(), method, a.webhookEndpoint(n, kind, suffix), key, body)
		if err != nil {
			return err
		}
		if kind.isFlexible() && action != "create" {
			removeResponseField(result, "signingKey")
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	bodyHelp := "Required JSON object or @file using API-specific fields"
	if kind.isFlexible() {
		bodyHelp += "; see nodit webhook flexible streams and schema"
	} else {
		bodyHelp += "; see nodit webhook classic event-types and schema"
	}
	cmd.Flags().StringVar(&flags.body, "body", "", bodyHelp)
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

func webhookBody(value string, kind webhookKind, action string) (json.RawMessage, error) {
	raw, err := readJSONBody(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, invalid("Webhook body must be a JSON object.")
	}
	if !kind.isFlexible() {
		return raw, nil
	}
	allowed, required := "name description status streamId filterExpression receiveFields destination", ""
	if action == "create" {
		required = "name streamId filterExpression destination"
	} else {
		allowed = "name description status"
	}
	for name := range fields {
		if !inWords(allowed, name) {
			return nil, invalid("Unsupported Flexible Webhook field: " + name + ".")
		}
	}
	for _, name := range strings.Fields(required) {
		if _, ok := fields[name]; !ok {
			return nil, invalid("Flexible Webhook create requires name, streamId, filterExpression, and destination.")
		}
	}
	if action == "update" && len(fields) == 0 {
		return nil, invalid("Flexible Webhook update body cannot be empty.")
	}
	if status, ok := fields["status"]; ok {
		var value string
		if json.Unmarshal(status, &value) != nil || !inWords("ACTIVE PAUSED", value) {
			return nil, invalid("Flexible Webhook status must be ACTIVE or PAUSED.")
		}
	}
	return raw, nil
}

func (a *app) webhookDeleteCommand(kind webhookKind) *cobra.Command {
	var flags webhookFlags
	cmd := &cobra.Command{Use: "delete <id>", Short: "Delete one webhook", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("network") {
			return invalid("Delete requires an explicit --network.")
		}
		if err := validID(args[0]); err != nil {
			return err
		}
		if err := a.confirm("Delete webhook "+args[0]+"?", flags.yes); err != nil {
			return err
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		result, _, err := a.apiRequest(cmd.Context(), http.MethodDelete, a.webhookEndpoint(n, kind, "/"+args[0]), key, nil)
		if err != nil {
			return err
		}
		if kind.isFlexible() {
			removeResponseField(result, "signingKey")
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	cmd.Flags().BoolVarP(&flags.yes, "yes", "y", false, "Skip the deletion confirmation")
	return cmd
}

func (a *app) confirm(prompt string, yes bool) error {
	if yes {
		return nil
	}
	if a.noInteractive {
		return failure("INTERACTION_REQUIRED", "Confirmation is required. Pass --yes to confirm without a prompt.")
	}
	_, _ = fmt.Fprint(a.stderr, prompt+" Type yes to continue: ")
	line, err := bufio.NewReader(io.LimitReader(a.stdin, 16)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return failure("INPUT_FAILED", "Cannot read confirmation.")
	}
	if strings.TrimSpace(line) != "yes" {
		return failure("CANCELLED", "Operation was not confirmed.")
	}
	return nil
}

func validID(id string) error {
	if id == "" || strings.IndexFunc(id, func(r rune) bool { return r <= ' ' || r >= 127 || r == '/' || r == '?' || r == '#' }) >= 0 {
		return invalid("ID must be a non-empty printable value without separators or whitespace.")
	}
	return nil
}

func removeResponseField(value any, field string) {
	switch value := value.(type) {
	case map[string]any:
		delete(value, field)
		for _, child := range value {
			removeResponseField(child, field)
		}
	case []any:
		for _, child := range value {
			removeResponseField(child, field)
		}
	}
}

func (a *app) classicHistoryCommand() *cobra.Command {
	var flags productFlags
	page, rpp := 1, 10
	var status, startAt, endAt, startSequence string
	var withMessage bool
	cmd := &cobra.Command{Use: "history <id>", Short: "List Classic Webhook delivery history", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		if err := validID(args[0]); err != nil {
			return err
		}
		if page < 1 || rpp < 1 || rpp > 100 {
			return invalid("--page must be positive and --rpp must be 1-100.")
		}
		if status != "" && !inWords("SUCCESS FAIL", status) {
			return invalid("--status must be SUCCESS or FAIL.")
		}
		for _, value := range []string{startAt, endAt} {
			if value != "" {
				if _, err := time.Parse(time.RFC3339, value); err != nil {
					return invalid("History times need a timezone, such as 2026-09-01T00:00:00Z.")
				}
			}
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		u, _ := url.Parse(a.webhookEndpoint(n, classicWebhook, "/history"))
		q := u.Query()
		q.Set("subscriptionId", args[0])
		q.Set("page", fmt.Sprint(page))
		q.Set("rpp", fmt.Sprint(rpp))
		if status != "" {
			q.Set("status", status)
		}
		if startAt != "" {
			q.Set("startAt", startAt)
		}
		if endAt != "" {
			q.Set("endAt", endAt)
		}
		if startSequence != "" {
			q.Set("startSequenceNumber", startSequence)
		}
		if cmd.Flags().Changed("with-event-message") {
			q.Set("withEventMessage", fmt.Sprint(withMessage))
		}
		u.RawQuery = q.Encode()
		result, _, err := a.apiRequest(cmd.Context(), http.MethodGet, u.String(), key, nil)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	cmd.Flags().IntVar(&page, "page", 1, "Page number")
	cmd.Flags().IntVar(&rpp, "rpp", 10, "Results per page, 1-100")
	cmd.Flags().StringVar(&status, "status", "", "SUCCESS or FAIL")
	cmd.Flags().StringVar(&startAt, "start-at", "", "Start time with a timezone, such as 2026-09-01T00:00:00Z")
	cmd.Flags().StringVar(&endAt, "end-at", "", "End time with a timezone, such as 2026-09-18T00:00:00Z")
	cmd.Flags().StringVar(&startSequence, "start-sequence-number", "", "Starting sequence number")
	cmd.Flags().BoolVar(&withMessage, "with-event-message", false, "Include event messages")
	return cmd
}

func (a *app) flexibleStreamsCommand() *cobra.Command {
	var flags productFlags
	page, rpp := 1, 10
	cmd := &cobra.Command{Use: "streams", Short: "List Flexible Webhook stream definitions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		if page < 1 || rpp < 1 || rpp > 100 {
			return invalid("--page must be positive and --rpp must be 1-100.")
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		u, _ := url.Parse(a.webhookEndpoint(n, flexibleWebhook, "/streams"))
		q := u.Query()
		q.Set("page", fmt.Sprint(page))
		q.Set("rpp", fmt.Sprint(rpp))
		u.RawQuery = q.Encode()
		result, _, err := a.apiRequest(cmd.Context(), http.MethodGet, u.String(), key, nil)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	cmd.Flags().IntVar(&page, "page", 1, "Page number")
	cmd.Flags().IntVar(&rpp, "rpp", 10, "Results per page, 1-100")
	return cmd
}

func (a *app) flexibleSchemaCommand() *cobra.Command {
	var flags productFlags
	cmd := &cobra.Command{Use: "schema <stream-id>", Short: "Get a Flexible Webhook stream schema", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		if err := validID(args[0]); err != nil {
			return err
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		result, _, err := a.apiRequest(cmd.Context(), http.MethodGet, a.webhookEndpoint(n, flexibleWebhook, "/streams/"+args[0]+"/schema"), key, nil)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	return cmd
}
