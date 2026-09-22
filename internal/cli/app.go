package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/noditlabs/nodit-cli/internal/buildinfo"
	"github.com/noditlabs/nodit-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	keyring "github.com/zalando/go-keyring"
)

type app struct {
	env             buildinfo.Environment
	config          configStore
	keys            credentialStore
	stdin           io.Reader
	stdout, stderr  io.Writer
	getenv          func(string) string
	now             func() time.Time
	httpClient      *http.Client
	streamDial      streamDialFunc
	openBrowser     func(string) error
	stdinRedirected bool
	format          string
	noInteractive   bool
	timeoutMS       int
}

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	env, err := buildinfo.Current()
	if err != nil {
		_ = output.Write(stderr, "yaml", map[string]any{"error": failure("INVALID_BUILD", "Build configuration is incomplete or invalid.")})
		return 1
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		_ = output.Write(stderr, "yaml", map[string]any{"error": failure("CONFIG_PATH_FAILED", "Cannot locate the user config directory.")})
		return 1
	}
	configDir := filepath.Join(dir, "nodit", env.Namespace)
	a := &app{
		env:    env,
		config: configStore{configDir},
		keys: fallbackCredentialStore{
			primary:  systemKeyring{"nodit-cli-" + env.Namespace},
			fallback: encryptedFileStore{configDir},
		},
		stdin:           stdin,
		stdout:          stdout,
		stderr:          stderr,
		getenv:          os.Getenv,
		now:             time.Now,
		httpClient:      &http.Client{},
		streamDial:      defaultStreamDial,
		openBrowser:     openBrowser,
		stdinRedirected: redirectedInput(stdin),
	}
	return a.execute(ctx, args)
}

func redirectedInput(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice == 0
}

func (a *app) execute(ctx context.Context, args []string) int {
	a.format = "yaml"
	if c, err := a.config.read(); err == nil && output.Valid(c.Output) {
		a.format = c.Output
	}
	// An unknown command fails before cobra binds --output, so the error would ignore
	// the flag the caller passed. Read it here so every failure honors the same order.
	a.applyOutputFlag(args)
	root := a.command()
	root.SetArgs(args)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil || errors.Is(err, errHelpShown) {
		return 0
	}
	var ce *commandError
	if !errors.As(err, &ce) {
		switch {
		case errors.Is(err, context.Canceled):
			ce = failure("CANCELLED", "Command cancelled.")
			ce.exit = 130
		case errors.Is(err, context.DeadlineExceeded):
			ce = failure("TIMEOUT", "Command timed out. Raise the limit with --timeout.")
		default:
			ce = usageError(cmd, err)
		}
	}
	if !output.Valid(a.format) {
		a.format = "yaml"
	}
	_ = output.Write(a.stderr, a.format, map[string]any{"error": ce})
	return ce.exit
}

// cobra names what is missing but not the command it belongs to, and the caller needs both. An
// unknown flag has neither, so it keeps the generic wording.
func usageError(cmd *cobra.Command, err error) *commandError {
	text := err.Error()
	if cmd == nil {
		return invalid("Invalid command or arguments. Run nodit --help.")
	}
	if rest, found := strings.CutPrefix(text, "required flag(s) "); found {
		names := strings.Split(strings.TrimSuffix(rest, " not set"), ", ")
		for i, name := range names {
			names[i] = "--" + strings.Trim(name, `"`)
		}
		message := "Missing " + strings.Join(names, " and ") + ". Run " + cmd.CommandPath() + " --help."
		if slices.Contains(names, "--project") {
			message += " List project IDs with nodit project list."
		}
		return invalid(message)
	}
	// cobra's argument validators all word the failure with "arg(s)".
	if strings.Contains(text, "arg(s)") {
		message := "Wrong number of arguments. Usage: " + strings.TrimSuffix(cmd.UseLine(), " [flags]") + "."
		if cmd.Example != "" {
			message += " Run " + cmd.CommandPath() + " --help for examples."
		} else {
			message += " Run " + cmd.CommandPath() + " --help."
		}
		return invalid(message)
	}
	// cobra quotes the name it could not resolve and can suggest near matches from the same tree.
	if name, found := quotedName(text, "unknown command "); found {
		// cobra applies its own default only on the path it suggests from itself, which is the root.
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if near := cmd.SuggestionsFor(name); len(near) > 0 {
			return invalid("Unknown command " + name + ". Did you mean " + strings.Join(near, ", ") + "?")
		}
		return invalid("Unknown command " + name + ". Run " + cmd.CommandPath() + " --help.")
	}
	return invalid("Invalid command or arguments. Run nodit --help.")
}

// A command that takes positional arguments answers a bare invocation with its help, the way a
// command group does. The check sits in the argument validator so it runs before required flags
// are enforced, and the sentinel ends the run without reporting a failure. Only the empty
// invocation is treated this way; a wrong number of arguments is still a usage error.
// cobra answers a command with no run of its own with the help before it validates arguments, so a
// group that only holds subcommands needs both: the run keeps the bare invocation printing help,
// and NoArgs lets a mistyped subcommand be reported instead of silently helping.
func asGroup(cmd *cobra.Command) *cobra.Command {
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	return cmd
}

var errHelpShown = errors.New("help shown")

func helpOnNoArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return validate(cmd, args)
		}
		if err := cmd.Help(); err != nil {
			return err
		}
		return errHelpShown
	}
}

func quotedName(text, prefix string) (string, bool) {
	rest, found := strings.CutPrefix(text, prefix)
	if !found {
		return "", false
	}
	// The name is quoted, and a single argument can carry spaces, so the closing quote ends it.
	// Cutting at the first space would report half of what was typed.
	rest = strings.TrimPrefix(rest, `"`)
	name, _, found := strings.Cut(rest, `"`)
	return name, found
}

// Unknown flags are tolerated: this pass only looks for --output and leaves every
// other validation to cobra.
func (a *app) applyOutputFlag(args []string) {
	set := pflag.NewFlagSet("output", pflag.ContinueOnError)
	set.ParseErrorsWhitelist.UnknownFlags = true
	set.SetOutput(io.Discard)
	format := set.StringP("output", "o", "", "")
	if set.Parse(args) == nil && output.Valid(*format) {
		a.format = *format
	}
}

func (a *app) success(value any) error {
	if err := output.Write(a.stdout, a.format, map[string]any{"data": value}); err != nil {
		return failure("OUTPUT_FAILED", "Cannot write command output.")
	}
	return nil
}

const rootLong = `Nodit CLI

Two credentials, picked by what you call:

  Management API (projects, API keys, allowlists, usage) uses a browser login.
    nodit auth login
    nodit project select <project-id>

  Product APIs (data, rpc, rest, webhook, stream) use an API key. Linking one with
  project select is enough; NODIT_API_KEY and --api-key override it.

Set a default network so --network can be left out, and check what is configured:

  nodit network list
  nodit config set network ethereum-mainnet
  nodit auth status

Shell completion: nodit completion --help`

func (a *app) command() *cobra.Command {
	var showVersion bool
	r := &cobra.Command{Use: "nodit", Short: "Nodit CLI", Long: rootLong, SilenceErrors: true, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showVersion {
				return a.version()
			}
			return cmd.Help()
		}}
	r.SetOut(a.stdout)
	r.SetErr(a.stderr)
	r.SetIn(a.stdin)
	r.PersistentFlags().StringVarP(&a.format, "output", "o", a.format, "Output format: yaml, json, jsonl, toon")
	_ = r.RegisterFlagCompletionFunc("output", completeWords("yaml json jsonl toon"))
	r.PersistentFlags().BoolVar(&a.noInteractive, "no-interactive", false, "Disable prompts and browser login")
	r.PersistentFlags().IntVar(&a.timeoutMS, "timeout", 30000, "HTTP request timeout in milliseconds")
	// cobra's own Version field prints outside the data envelope and ignores --output,
	// so the flag routes to the same handler as the subcommand.
	r.Flags().BoolVar(&showVersion, "version", false, "Show version")
	// The root help does not list a subcommand's flags, so the command that rejected the flag is
	// the one worth naming, the same way a missing required flag names it.
	r.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return invalid("Invalid flag or flag value. Run " + cmd.CommandPath() + " --help.")
	})
	r.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if !output.Valid(a.format) {
			return invalid("Output must be yaml, json, jsonl, or toon.")
		}
		if a.timeoutMS <= 0 || a.timeoutMS > 3600000 {
			return invalid("Timeout must be between 1 and 3600000 milliseconds.")
		}
		return nil
	}
	r.AddCommand(a.configCommand(), a.networkCommand(), a.authCommand(), a.projectCommand(), a.apiKeyCommand(), a.usageCommand(), a.allowlistCommand(), a.dataCommand(), a.rpcCommand(), a.restCommand(), a.webhookCommand(), a.streamCommand(), &cobra.Command{Use: "version", Short: "Show version", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		return a.version()
	}})
	return r
}

func (a *app) version() error {
	return a.success(map[string]any{"version": buildinfo.ReportedVersion()})
}

func (a *app) configCommand() *cobra.Command {
	r := asGroup(&cobra.Command{Use: "config", Short: "Manage local defaults"})
	r.AddCommand(&cobra.Command{Use: "path", Short: "Show the config file path", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		return a.success(map[string]any{"path": a.config.path()})
	}})
	r.AddCommand(&cobra.Command{Use: "list", Short: "Show saved defaults; run nodit auth status for credential state", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		c, err := a.config.read()
		if err != nil {
			return err
		}
		// The stored struct omits empty fields, so reading it back cannot tell an unset
		// key from a missing one. The listing names every key instead.
		keys := map[string]any{"network": nil, "output": nil, "project": nil, "projectKeys": map[string]string{}}
		if c.Network != "" {
			keys["network"] = c.Network
		}
		if c.Output != "" {
			keys["output"] = c.Output
		}
		if c.Project != "" {
			keys["project"] = c.Project
		}
		if len(c.ProjectKeys) > 0 {
			keys["projectKeys"] = c.ProjectKeys
		}
		return a.success(keys)
	}})
	r.AddCommand(&cobra.Command{Use: "get <key>", Short: "Get network, output, or selected project", Args: helpOnNoArgs(cobra.ExactArgs(1)), ValidArgsFunction: completeConfigArgs, RunE: func(_ *cobra.Command, args []string) error {
		c, err := a.config.read()
		if err != nil {
			return err
		}
		var value any
		switch args[0] {
		case "network":
			if c.Network != "" {
				value = c.Network
			}
		case "output":
			value = c.Output
			if c.Output == "" {
				value = "yaml"
			}
		case "project":
			if c.Project != "" {
				value = c.Project
			}
		default:
			return invalid("Config key must be network, output, or project.")
		}
		return a.success(map[string]any{args[0]: value})
	}})
	r.AddCommand(&cobra.Command{Use: "set <key> <value>", Short: "Set network or output", Args: helpOnNoArgs(cobra.ExactArgs(2)), ValidArgsFunction: completeConfigArgs, RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]
		switch key {
		case "network":
			if _, err := findNetwork(value); err != nil {
				return err
			}
		case "output":
			if !output.Valid(value) {
				return invalid("Output must be yaml, json, jsonl, or toon.")
			}
		case "project":
			return invalid("Select a project with nodit project select instead of config set.")
		default:
			return invalid("Config key must be network or output.")
		}
		err := a.config.update(cmd.Context(), func(c *config) error {
			if key == "network" {
				c.Network = value
			} else {
				c.Output = value
			}
			return nil
		})
		if err != nil {
			return err
		}
		return a.success(map[string]any{key: value})
	}})
	r.AddCommand(&cobra.Command{Use: "unset <key>", Short: "Remove a saved network or output default", Args: helpOnNoArgs(cobra.ExactArgs(1)), ValidArgsFunction: completeConfigArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "network" && args[0] != "output" && args[0] != "project" {
			return invalid("Config key must be network, output, or project.")
		}
		err := a.config.update(cmd.Context(), func(c *config) error {
			if args[0] == "network" {
				c.Network = ""
			} else if args[0] == "output" {
				c.Output = ""
			} else {
				c.Project = ""
			}
			return nil
		})
		if err != nil {
			return err
		}
		return a.success(map[string]any{args[0]: nil})
	}})
	return r
}

func (a *app) networkCommand() *cobra.Command {
	r := asGroup(&cobra.Command{Use: "network", Short: "Inspect the bundled public network catalog"})
	var chain, product string
	list := &cobra.Command{Use: "list", Short: "List supported networks", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		items, err := filterNetworks(chain, product)
		if err != nil {
			return err
		}
		return a.success(map[string]any{"networks": items})
	}}
	list.Flags().StringVar(&chain, "chain", "", "Filter by chain identifier")
	list.Flags().StringVar(&product, "product", "", "Filter by product: node, data, webhook, stream")
	_ = list.RegisterFlagCompletionFunc("chain", completeChain)
	_ = list.RegisterFlagCompletionFunc("product", completeWords("node data webhook stream"))
	get := &cobra.Command{Use: "get <id>", Short: "Show a supported network", Args: helpOnNoArgs(cobra.ExactArgs(1)), ValidArgsFunction: func(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return suggest(networkIDs(""), prefix)
	}, RunE: func(_ *cobra.Command, args []string) error {
		n, err := findNetwork(args[0])
		if err != nil {
			return err
		}
		return a.success(n)
	}}
	r.AddCommand(list, get)
	return r
}

func (a *app) credentialPresent(key string) (bool, error) {
	value, err := a.keys.Get(key)
	if errors.Is(err, keyring.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, storageError()
	}
	return value != "", nil
}
