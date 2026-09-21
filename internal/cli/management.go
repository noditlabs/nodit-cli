package cli

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	keyring "github.com/zalando/go-keyring"
)

var (
	uuidPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	usagePeriodPattern = regexp.MustCompile(`^[1-9][0-9]*[mhdw]$`)
)

// The shortest window that can be answered: rows are five minutes wide and the confirmed boundary
// trails the clock by up to two of them, so a shorter window holds nothing confirmed.
const usageMinPeriod = 10 * time.Minute

func (a *app) managementRequest(cmd *cobra.Command, method, path string, query url.Values, body any) (any, error) {
	token, err := a.accessToken(cmd.Context())
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(a.env.Resource + "/v1" + path)
	if err != nil {
		return nil, failure("INVALID_ENDPOINT", "Cannot create Management API request.")
	}
	u.RawQuery = query.Encode()
	result, _, err := a.jsonRequest(cmd.Context(), method, u.String(), "Authorization", "Bearer "+token, body)
	return result, err
}

func projectID(value string) error {
	if len(value) < 1 || len(value) > 20 || !decimal(value) {
		return invalid("Project ID must contain 1-20 decimal digits.")
	}
	return nil
}

func keyID(value string) error {
	if !uuidPattern.MatchString(value) {
		return invalid("API key ID must be a UUID.")
	}
	return nil
}

func setOptional(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

func (a *app) projectCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "project", Short: "Inspect and select account projects using OAuth"})
	var all bool
	list := &cobra.Command{
		Use: "list", Short: "List account projects", Args: cobra.NoArgs,
		Long: "List account projects.\nOnly running projects are listed unless --all is given.\n" +
			"The endpoint has no status filter, so the others are dropped here and count is the number listed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := a.managementRequest(cmd, http.MethodGet, "/projects", url.Values{}, nil)
			if err != nil {
				return err
			}
			if all {
				return a.success(result)
			}
			return a.success(onlyRunning(result))
		},
	}
	list.Flags().BoolVar(&all, "all", false, "Include deleted projects")
	root.AddCommand(list, a.projectSelectCommand(), a.projectNetworksCommand())
	return root
}

// `count` is recomputed because the server's total describes a list this command did not print.
func onlyRunning(result any) any {
	envelope, ok := result.(map[string]any)
	if !ok {
		return result
	}
	items, ok := envelope["items"].([]any)
	if !ok {
		return result
	}
	kept := make([]any, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok && entry["status"] != "RUNNING" {
			continue
		}
		kept = append(kept, item)
	}
	filtered := make(map[string]any, len(envelope))
	for name, value := range envelope {
		filtered[name] = value
	}
	filtered["items"] = kept
	filtered["count"] = len(kept)
	return filtered
}

// Here `--network` is the network half of an ID, unlike the product commands where it is the whole
// ID. Accepting either shape keeps one meaning across the CLI; a full ID carries its own protocol.
func splitNetworkFilter(protocol, network string) (string, string, error) {
	n, err := findNetwork(network)
	if err != nil {
		if network != "" && protocol == "" {
			return "", "", invalid("--network requires --protocol, or pass a full network ID.")
		}
		return protocol, network, nil
	}
	if protocol != "" && protocol != n.Chain {
		return "", "", invalid("--protocol does not match --network.")
	}
	return n.Chain, n.Network, nil
}

func (a *app) projectNetworksCommand() *cobra.Command {
	var project, protocol, network string
	page, rpp := 1, 20
	cmd := &cobra.Command{Use: "networks", Short: "List account project networks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if project != "" {
			if err := projectID(project); err != nil {
				return err
			}
		}
		filterProtocol, filterNetwork, err := splitNetworkFilter(protocol, network)
		if err != nil {
			return err
		}
		if page < 1 || rpp < 1 || rpp > 1000 {
			return invalid("--page must be positive and --rpp must be 1-1000.")
		}
		q := url.Values{"page": {fmt.Sprint(page)}, "rpp": {fmt.Sprint(rpp)}}
		setOptional(q, "projectId", project)
		setOptional(q, "protocol", filterProtocol)
		setOptional(q, "network", filterNetwork)
		result, err := a.managementRequest(cmd, http.MethodGet, "/chains", q, nil)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID, such as 1712893835263803228; account-wide when omitted")
	cmd.Flags().StringVar(&protocol, "protocol", "", "Protocol filter, such as ethereum")
	cmd.Flags().StringVar(&network, "network", "", "Network filter: a full ID, or the network half with --protocol")
	cmd.Flags().IntVar(&page, "page", 1, "Page number")
	cmd.Flags().IntVar(&rpp, "rpp", 20, "Results per page, 1-1000")
	return cmd
}

func keyChoices(keys []map[string]any) []map[string]any {
	choices := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		choice := map[string]any{"keyId": stringField(key, "keyId")}
		for _, name := range []string{"name", "maskedValue"} {
			if value := stringField(key, name); value != "" {
				choice[name] = value
			}
		}
		choices = append(choices, choice)
	}
	return choices
}

func keyCount(parseErr error, keys []map[string]any) string {
	if parseErr != nil {
		return "an unreadable API key list"
	}
	if len(keys) == 0 {
		return "no active API key"
	}
	return fmt.Sprintf("%d active API keys", len(keys))
}

func (a *app) projectSelectCommand() *cobra.Command {
	var selectedKeyID string
	cmd := &cobra.Command{Use: "select <id>", Short: "Select a project and securely link one active API key", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		if err := projectID(id); err != nil {
			return err
		}
		projects, err := a.managementRequest(cmd, http.MethodGet, "/projects", url.Values{"projectId": {id}}, nil)
		if err != nil {
			return err
		}
		items, err := objectItems(projects)
		if err != nil || len(items) != 1 || stringField(items[0], "projectId") != id {
			return failure("PROJECT_NOT_FOUND", "Project was not found in this account. List them with nodit project list.")
		}
		if stringField(items[0], "status") == "DELETED" {
			return failure("PROJECT_NOT_ACTIVE", "A deleted project cannot be selected. List running projects with nodit project list.")
		}
		keyToUse := selectedKeyID
		if keyToUse == "" {
			q := url.Values{"projectId": {id}, "status": {"ACTIVE"}, "page": {"1"}, "rpp": {"1000"}}
			listed, err := a.managementRequest(cmd, http.MethodGet, "/api-keys", q, nil)
			if err != nil {
				return err
			}
			keys, parseErr := objectItems(listed)
			if parseErr != nil || len(keys) != 1 {
				e := failure("API_KEY_SELECTION_REQUIRED", "Pass --key-id: the project has "+keyCount(parseErr, keys)+".")
				e.Details = keyChoices(keys)
				return e
			}
			keyToUse = stringField(keys[0], "keyId")
		}
		if err := keyID(keyToUse); err != nil {
			return err
		}
		detail, err := a.managementRequest(cmd, http.MethodGet, "/api-keys/"+keyToUse, url.Values{}, nil)
		if err != nil {
			return err
		}
		key, ok := detail.(map[string]any)
		if !ok || stringField(key, "keyId") != keyToUse || stringField(key, "projectId") != id || stringField(key, "status") != "ACTIVE" || stringField(key, "value") == "" {
			return failure(
				"INVALID_PROJECT_API_KEY",
				"The API key is not active, has no value, or belongs to another project. "+
					"List active keys with nodit apikey list --project "+id+".",
			)
		}
		credentialName := projectCredentialKey(id, keyToUse)
		previous, previousErr := a.keys.Get(credentialName)
		if previousErr != nil && !errors.Is(previousErr, keyring.ErrNotFound) {
			return storageError()
		}
		err = a.config.update(cmd.Context(), func(c *config) error {
			if a.keys.Set(credentialName, stringField(key, "value")) != nil {
				return storageError()
			}
			if c.ProjectKeys == nil {
				c.ProjectKeys = map[string]string{}
			}
			c.ProjectKeys[id] = keyToUse
			c.Project = id
			return nil
		})
		if err != nil {
			if previousErr == nil {
				_ = a.keys.Set(credentialName, previous)
			} else {
				_ = a.keys.Delete(credentialName)
			}
			return err
		}
		return a.success(map[string]any{"projectId": id, "keyId": keyToUse, "selected": true})
	}}
	cmd.Flags().StringVar(&selectedKeyID, "key-id", "", "Active API key UUID, such as 3f0a1c22-...; optional when the project has exactly one")
	return cmd
}

func objectItems(value any) ([]map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("not an object")
	}
	raw, ok := object["items"].([]any)
	if !ok {
		return nil, errors.New("items missing")
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("invalid item")
		}
		result = append(result, object)
	}
	return result, nil
}

func stringField(value map[string]any, name string) string {
	result, _ := value[name].(string)
	return result
}

func (a *app) apiKeyCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "apikey", Short: "Inspect account API keys using OAuth"})
	var project, status string
	var all bool
	page, rpp := 1, 20
	list := &cobra.Command{
		Use: "list", Short: "List masked API keys", Args: cobra.NoArgs,
		Long: "List masked API keys.\nOnly active keys are listed unless --status or --all is given.\n" +
			"The endpoint filters by status itself, so count matches what is listed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project != "" {
				if err := projectID(project); err != nil {
					return err
				}
			}
			if status != "" && !inWords("ACTIVE DELETED", status) {
				return invalid("--status must be ACTIVE or DELETED.")
			}
			if all && status != "" {
				return invalid("Use --status or --all, not both.")
			}
			if page < 1 || rpp < 1 || rpp > 1000 {
				return invalid("--page must be positive and --rpp must be 1-1000.")
			}
			q := url.Values{"page": {fmt.Sprint(page)}, "rpp": {fmt.Sprint(rpp)}}
			setOptional(q, "projectId", project)
			// Filtering at the endpoint keeps `count` describing what is printed.
			if !all {
				if status == "" {
					status = "ACTIVE"
				}
				setOptional(q, "status", status)
			}
			result, err := a.managementRequest(cmd, http.MethodGet, "/api-keys", q, nil)
			if err != nil {
				return err
			}
			return a.success(strippedAPIKeyValues(result))
		},
	}
	list.Flags().StringVarP(&project, "project", "p", "", "Project ID, such as 1712893835263803228; account-wide when omitted")
	list.Flags().StringVar(&status, "status", "", "ACTIVE or DELETED; active only when omitted")
	list.Flags().BoolVar(&all, "all", false, "List every status, including deleted keys")
	list.Flags().IntVar(&page, "page", 1, "Page number")
	list.Flags().IntVar(&rpp, "rpp", 20, "Results per page, 1-1000")
	get := &cobra.Command{Use: "get <key-id>", Short: "Get API key metadata with its value masked", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		if err := keyID(args[0]); err != nil {
			return err
		}
		result, err := a.managementRequest(cmd, http.MethodGet, "/api-keys/"+args[0], url.Values{}, nil)
		if err != nil {
			return err
		}
		object, ok := result.(map[string]any)
		if !ok {
			return failure("INVALID_API_RESPONSE", "Management API returned an invalid API key.")
		}
		return a.success(strippedAPIKeyValues(object))
	}}
	root.AddCommand(list, get)
	return root
}

func strippedAPIKeyValues(v any) any {
	switch x := v.(type) {
	case []any:
		for i, item := range x {
			x[i] = strippedAPIKeyValues(item)
		}
	case map[string]any:
		delete(x, "value")
		for name, item := range x {
			x[name] = strippedAPIKeyValues(item)
		}
	}
	return v
}

type usageFlags struct {
	project, from, to, period, protocol, network, granularity string
	requestTypes, groupBy                                     []string
	page, rpp                                                 int
}

func bindUsageFlags(cmd *cobra.Command, f *usageFlags, kind string) {
	cmd.Flags().StringVarP(&f.project, "project", "p", "", "Project ID, such as 1712893835263803228; account-wide when omitted")
	cmd.Flags().StringVar(&f.from, "from", "", "Range start, such as 2026-09-01T00:00:00Z")
	cmd.Flags().StringVar(&f.to, "to", "", "Range end, such as 2026-09-18T00:00:00Z")
	cmd.Flags().StringVar(&f.period, "period", "", "Relative period ending now, such as 30m, 24h, 7d, 4w")
	cmd.Flags().StringVar(&f.protocol, "protocol", "", "Protocol filter, such as ethereum")
	cmd.Flags().StringVar(&f.network, "network", "", "Network filter: a full ID, or the network half with --protocol")
	_ = cmd.RegisterFlagCompletionFunc("network", func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return suggest(networkIDs(""), prefix)
	})
	cmd.Flags().StringSliceVar(&f.requestTypes, "request-type", nil, "NODE_API, WEB3_DATA_API, APTOS_INDEXER_API, WEBHOOK, or STREAM (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("request-type", completeWords("NODE_API WEB3_DATA_API APTOS_INDEXER_API WEBHOOK STREAM"))
	if kind == "timeseries" {
		cmd.Flags().StringVar(&f.granularity, "granularity", "1d", "5m, 1h, or 1d")
		_ = cmd.RegisterFlagCompletionFunc("granularity", completeWords("5m 1h 1d"))
	}
	if kind == "breakdown" {
		cmd.Flags().StringSliceVar(&f.groupBy, "group-by", nil, "Required PROJECT and/or CHAIN")
		_ = cmd.RegisterFlagCompletionFunc("group-by", completeWords("PROJECT CHAIN"))
		cmd.Flags().IntVar(&f.page, "page", 1, "Page number")
		cmd.Flags().IntVar(&f.rpp, "rpp", 20, "Results per page, 1-1000")
	}
}

func usageQuery(f usageFlags, kind string) (url.Values, error) {
	if err := validateUsageFlags(f, kind); err != nil {
		return nil, err
	}
	q := url.Values{}
	setOptional(q, "projectId", f.project)
	setOptional(q, "from", f.from)
	setOptional(q, "to", f.to)
	setOptional(q, "period", f.period)
	filterProtocol, filterNetwork, err := splitNetworkFilter(f.protocol, f.network)
	if err != nil {
		return nil, err
	}
	setOptional(q, "protocol", filterProtocol)
	setOptional(q, "network", filterNetwork)
	for _, value := range f.requestTypes {
		q.Add("requestType", value)
	}
	if kind == "timeseries" {
		q.Set("granularity", f.granularity)
	}
	if kind == "breakdown" {
		for _, value := range f.groupBy {
			q.Add("groupBy", value)
		}
		q.Set("page", fmt.Sprint(f.page))
		q.Set("rpp", fmt.Sprint(f.rpp))
	}
	return q, nil
}

// usagePeriodDuration parses a --period value the pattern has already accepted, such as 30m, 24h, 7d
// or 4w. A value that does not fit in a Duration is rejected rather than wrapped: the wrapped number
// is small and positive often enough to read as a window far shorter than the one that was asked for.
func usagePeriodDuration(period string) (time.Duration, error) {
	amount, err := strconv.ParseInt(period[:len(period)-1], 10, 64)
	if err != nil {
		return 0, err
	}
	var unit time.Duration
	switch period[len(period)-1] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("unsupported period unit %q", period[len(period)-1:])
	}
	if amount > int64(math.MaxInt64)/int64(unit) {
		return 0, fmt.Errorf("period %s is out of range", period)
	}
	return time.Duration(amount) * unit, nil
}

func validateUsageFlags(f usageFlags, kind string) error {
	if f.project != "" {
		if err := projectID(f.project); err != nil {
			return err
		}
	}
	if f.period != "" && (f.from != "" || f.to != "") {
		return invalid("--period cannot be combined with --from or --to.")
	}
	var parsed [2]time.Time
	for i, value := range []string{f.from, f.to} {
		if value != "" {
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return invalid("Range times need a timezone, such as 2026-09-01T00:00:00Z.")
			}
			parsed[i] = t
		}
	}
	if !parsed[0].IsZero() && !parsed[1].IsZero() && !parsed[0].Before(parsed[1]) {
		return invalid("--from must be earlier than --to.")
	}
	if f.period != "" {
		if !usagePeriodPattern.MatchString(f.period) {
			return invalid("--period must be a positive value such as 24h or 7d.")
		}
		d, err := usagePeriodDuration(f.period)
		if err != nil {
			return invalid("--period is out of the supported range.")
		}
		if d < usageMinPeriod {
			return invalid("--period must cover at least 10m.")
		}
	}
	for _, value := range f.requestTypes {
		if !inWords("NODE_API WEB3_DATA_API APTOS_INDEXER_API WEBHOOK STREAM", value) {
			return invalid("Unsupported --request-type.")
		}
	}
	if kind == "timeseries" {
		if !inWords("5m 1h 1d", f.granularity) {
			return invalid("--granularity must be 5m, 1h, or 1d.")
		}
	}
	if kind == "breakdown" {
		if len(f.groupBy) == 0 {
			return invalid("Usage breakdown requires --group-by PROJECT and/or CHAIN.")
		}
		seen := map[string]bool{}
		for _, value := range f.groupBy {
			if !inWords("PROJECT CHAIN", value) || seen[value] {
				return invalid("--group-by accepts unique PROJECT and CHAIN values.")
			}
			seen[value] = true
		}
		if f.page < 1 || f.rpp < 1 || f.rpp > 1000 {
			return invalid("--page must be positive and --rpp must be 1-1000.")
		}
	}
	return nil
}

const usageLong = "Usage figures are for reference. The billing statement is the record of account usage,\n" +
	"and these figures can lag it because aggregation is not immediate.\n" +
	"Dedicated node traffic is excluded from Compute Unit accounting and is not counted here;\n" +
	"check dedicated node usage in the console.\n" +
	"Compute Unit history reaches as far back as the account plan allows, while request counts\n" +
	"reach back 40 days at most regardless of plan.\n" +
	"Answers stop at the last confirmed five-minute boundary, whatever --to asks for and even\n" +
	"when it is left unset, so both usedCu and requests cover the same window and the last few\n" +
	"minutes are missing from both. A range lying entirely past that boundary is rejected, and\n" +
	"--period must cover at least 10m.\n" +
	"requests is null when the range reaches past the 40 day count history, when --request-type\n" +
	"names WEBHOOK, STREAM or WEB3_DATA_API, which carry no request counts, and while counting\n" +
	"is unreadable or has not reached the current billing cycle. That last case leaves the\n" +
	"window uncut, so usedCu still answers the cycle it was asked for.\n" +
	"coveredThrough is the end of the window the answer covers, so comparing it with the --to that\n" +
	"was sent shows whether the range was cut. Plan limits are the account as it stands and are\n" +
	"unaffected by the range."

const usageTimeseriesLong = "Buckets are cut on UTC boundaries, and the bucket that straddles the confirmed\n" +
	"boundary reports requests as null while usedCu keeps growing on re-query.\n" +
	"A 5m range cannot exceed one day, so pass --period or --from with that granularity."

func (a *app) usageCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "usage", Short: "Inspect account Compute Unit usage using OAuth", Long: usageLong})
	for _, kind := range []string{"summary", "timeseries", "breakdown"} {
		kind := kind
		var flags usageFlags
		long := "Get usage " + kind + ".\n\n" + usageLong
		if kind == "timeseries" {
			long += "\n\n" + usageTimeseriesLong
		}
		cmd := &cobra.Command{Use: kind, Short: "Get usage " + kind, Long: long, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := usageQuery(flags, kind)
			if err != nil {
				return err
			}
			result, err := a.managementRequest(cmd, http.MethodGet, "/usage/"+kind, q, nil)
			if err != nil {
				return err
			}
			return a.success(result)
		}}
		bindUsageFlags(cmd, &flags, kind)
		root.AddCommand(cmd)
	}
	return root
}

func (a *app) allowlistCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "allowlist", Short: "Manage project IP and domain restrictions using OAuth"})
	var project string
	list := &cobra.Command{Use: "list", Short: "Get allowlist settings and entries", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := projectID(project); err != nil {
			return err
		}
		result, err := a.managementRequest(cmd, http.MethodGet, "/allowlist", url.Values{"projectId": {project}}, nil)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	list.Flags().StringVarP(&project, "project", "p", "", "Required project ID, such as 1712893835263803228")
	_ = list.MarkFlagRequired("project")
	root.AddCommand(list, a.allowlistSetCommand(), a.allowlistEntryCommand(true), a.allowlistEntryCommand(false))
	return root
}

func (a *app) allowlistSetCommand() *cobra.Command {
	var project, matchRule string
	var ipRestrict, domainRestrict bool
	cmd := &cobra.Command{Use: "set", Short: "Update allowlist restriction settings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := projectID(project); err != nil {
			return err
		}
		body := map[string]any{}
		if cmd.Flags().Changed("ip-restrict") {
			body["ip"] = map[string]bool{"restrict": ipRestrict}
		}
		if cmd.Flags().Changed("domain-restrict") {
			body["domain"] = map[string]bool{"restrict": domainRestrict}
		}
		if cmd.Flags().Changed("match-rule") {
			if !inWords("AND OR", matchRule) {
				return invalid("--match-rule must be AND or OR.")
			}
			body["matchRule"] = matchRule
		}
		if len(body) == 0 {
			return invalid("Set at least one restriction flag or match rule.")
		}
		result, err := a.managementRequest(cmd, http.MethodPatch, "/allowlist", url.Values{"projectId": {project}}, body)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	cmd.Flags().StringVarP(&project, "project", "p", "", "Required project ID, such as 1712893835263803228")
	cmd.Flags().BoolVar(&ipRestrict, "ip-restrict", false, "Enable or disable IP restriction")
	cmd.Flags().BoolVar(&domainRestrict, "domain-restrict", false, "Enable or disable domain restriction")
	cmd.Flags().StringVar(&matchRule, "match-rule", "", "AND or OR")
	_ = cmd.RegisterFlagCompletionFunc("match-rule", completeWords("AND OR"))
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func (a *app) allowlistEntryCommand(add bool) *cobra.Command {
	action, method, short := "add", http.MethodPost, "Add one IP or domain entry"
	if !add {
		action, method, short = "remove", http.MethodDelete, "Remove one IP or domain entry"
	}
	var project, kind, name string
	var yes bool
	cmd := &cobra.Command{Use: action + " <value>", Short: short, Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		if err := projectID(project); err != nil {
			return err
		}
		if !inWords("ip domain", kind) {
			return invalid("--type must be ip or domain.")
		}
		if strings.TrimSpace(args[0]) == "" {
			return invalid("Allowlist value cannot be empty.")
		}
		if !add {
			if err := a.confirm("Remove allowlist entry "+args[0]+"?", yes); err != nil {
				return err
			}
		}
		path := "/allowlist/" + kind + "s"
		q := url.Values{"projectId": {project}}
		var body any
		if add {
			body = map[string]any{"value": args[0]}
			if name != "" {
				body.(map[string]any)["name"] = name
			}
		} else {
			q.Set("value", args[0])
		}
		result, err := a.managementRequest(cmd, method, path, q, body)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	cmd.Flags().StringVarP(&project, "project", "p", "", "Required project ID, such as 1712893835263803228")
	cmd.Flags().StringVar(&kind, "type", "", "Required entry type: ip or domain")
	_ = cmd.RegisterFlagCompletionFunc("type", completeWords("ip domain"))
	if add {
		cmd.Flags().StringVar(&name, "name", "", "Optional unique label")
	} else {
		cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip removal confirmation")
	}
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}
