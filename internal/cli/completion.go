package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// Product behind each top-level command, so a suggested network is one the command can reach.
var completionProducts = map[string]string{
	"data": "data", "rpc": "node", "rest": "node", "webhook": "webhook", "stream": "stream",
}

func suggest(values []string, prefix string) ([]string, cobra.ShellCompDirective) {
	matched := make([]string, 0, len(values))
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			matched = append(matched, v)
		}
	}
	return matched, cobra.ShellCompDirectiveNoFileComp
}

func completeWords(words string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return suggest(strings.Fields(words), prefix)
	}
}

func networkIDs(product string) []string {
	ids := make([]string, 0, len(networks))
	for _, n := range networks {
		if product == "" || inWords(strings.Join(n.Products, " "), product) {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

// The command tree is complete by the time completion runs, so the product is read from the group
// rather than passed into all sixteen bindings.
func completeNetwork(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	group := cmd
	for group != nil && group.Parent() != nil && group.Parent().Parent() != nil {
		group = group.Parent()
	}
	product := ""
	if group != nil {
		product = completionProducts[group.Name()]
	}
	return suggest(networkIDs(product), prefix)
}

func completeChain(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	seen := map[string]bool{}
	chains := make([]string, 0)
	for _, n := range networks {
		if !seen[n.Chain] {
			seen[n.Chain] = true
			chains = append(chains, n.Chain)
		}
	}
	return suggest(chains, prefix)
}

func completeConfigArgs(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	switch {
	case len(args) == 0:
		return suggest([]string{"network", "output", "project"}, prefix)
	case len(args) == 1 && args[0] == "network":
		return suggest(networkIDs(""), prefix)
	case len(args) == 1 && args[0] == "output":
		return suggest([]string{"yaml", "json", "jsonl", "toon"}, prefix)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
