package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/noditlabs/nodit-cli/internal/buildinfo"
	keyring "github.com/zalando/go-keyring"
)

type fakeKeys struct {
	mu     sync.Mutex
	values map[string]string
	err    error
}

func (s *fakeKeys) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	value, ok := s.values[key]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (s *fakeKeys) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.values[key] = value
	return nil
}
func (s *fakeKeys) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	delete(s.values, key)
	return nil
}

func newTestApp(t *testing.T) *app {
	t.Helper()
	env := testEnvironment(t, "test")
	return &app{env: env, config: configStore{t.TempDir()}, keys: &fakeKeys{values: map[string]string{}}, stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(string) string { return "" }, now: time.Now, httpClient: &http.Client{}, timeoutMS: 30000, format: "json", openBrowser: func(string) error { t.Error("unexpected browser open"); return errors.New("unexpected") }}
}

func testEnvironment(t *testing.T, suffix string) buildinfo.Environment {
	t.Helper()
	env, err := buildinfo.NewEnvironment("https://auth."+suffix+".example.test", "https://api."+suffix+".example.test", suffix+".example.test")
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func run(t *testing.T, a *app, args ...string) (int, string, string) {
	t.Helper()
	a.stdout, a.stderr = &bytes.Buffer{}, &bytes.Buffer{}
	code := a.execute(context.Background(), args)
	return code, a.stdout.(*bytes.Buffer).String(), a.stderr.(*bytes.Buffer).String()
}

func TestVersionFlagMatchesSubcommand(t *testing.T) {
	a := newTestApp(t)
	code, flagOut, stderr := run(t, a, "--version")
	if code != 0 || stderr != "" || !strings.Contains(flagOut, "version") {
		t.Fatalf("%d %s %s", code, flagOut, stderr)
	}
	code, subOut, stderr := run(t, a, "version")
	if code != 0 || stderr != "" || subOut != flagOut {
		t.Fatalf("flag and subcommand differ: %q %q %s", flagOut, subOut, stderr)
	}
}

func TestConfigListNamesEveryKeyWhenUnset(t *testing.T) {
	a := newTestApp(t)
	code, out, stderr := run(t, a, "config", "list", "-o", "json")
	if code != 0 || stderr != "" {
		t.Fatalf("%d %s", code, stderr)
	}
	for _, key := range []string{"network", "output", "project", "projectKeys"} {
		if !strings.Contains(out, `"`+key+`"`) {
			t.Fatalf("%s missing from an empty listing: %s", key, out)
		}
	}
	if code, _, stderr := run(t, a, "config", "set", "network", "ethereum-mainnet"); code != 0 {
		t.Fatal(stderr)
	}
	if code, out, _ := run(t, a, "config", "list", "-o", "json"); code != 0 || !strings.Contains(out, "ethereum-mainnet") || !strings.Contains(out, `"output"`) {
		t.Fatalf("%d %s", code, out)
	}
}

func TestRedirectedInputDetection(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if !redirectedInput(file) {
		t.Fatal("redirected file was not detected")
	}
	if redirectedInput(strings.NewReader("[]")) {
		t.Fatal("generic reader was treated as process stdin")
	}
}

func TestConfigPersistenceAndValidation(t *testing.T) {
	a := newTestApp(t)
	if c, _, e := run(t, a, "config", "set", "network", "solana-devnet"); c != 0 {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(a.config.path())
	if c, out, e := run(t, a, "config", "set", "network", "solana-testnet", "-o", "json"); c != 2 || out != "" || !strings.Contains(e, "UNSUPPORTED_NETWORK") || strings.Contains(e, "update") {
		t.Fatalf("%d %s %s", c, out, e)
	}
	after, _ := os.ReadFile(a.config.path())
	if !bytes.Equal(before, after) {
		t.Fatal("invalid config mutated disk")
	}
	info, err := os.Stat(a.config.path())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v %v", info, err)
	}
	if c, _, e := run(t, a, "config", "set", "output", "jsonl"); c != 0 {
		t.Fatal(e)
	}
	if c, out, e := run(t, a, "network", "get", "solana-devnet"); c != 0 || strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, `{"data":`) {
		t.Fatalf("%d %s %s", c, out, e)
	}
	if c, out, e := run(t, a, "config", "get", "output", "-o", "yaml"); c != 0 || !strings.HasPrefix(out, "data:") || !strings.Contains(out, "jsonl") {
		t.Fatalf("%d %s %s", c, out, e)
	}
	for i := 0; i < 2; i++ {
		if c, _, e := run(t, a, "config", "unset", "network"); c != 0 {
			t.Fatal(e)
		}
	}
	c, err := a.config.read()
	if err != nil || c.Network != "" || c.Output != "jsonl" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestNetworkPrecedenceAndCatalog(t *testing.T) {
	for _, tc := range []struct{ flag, env, saved, want string }{
		{"solana-devnet", "ethereum-mainnet", "aptos-testnet", "solana-devnet"},
		{"", "ethereum-mainnet", "aptos-testnet", "ethereum-mainnet"},
		{"", "", "aptos-testnet", "aptos-testnet"},
	} {
		got, err := resolveNetwork(tc.flag, tc.env, config{Network: tc.saved})
		if err != nil || got != tc.want {
			t.Fatalf("%s %v", got, err)
		}
	}
	if _, err := resolveNetwork("", "", config{}); err == nil {
		t.Fatal("implicit network")
	}
	if _, err := resolveNetwork("unknown", "ethereum-mainnet", config{}); err == nil {
		t.Fatal("invalid flag fell back")
	}
	for _, id := range []string{"polygon-amoy", "sui-testnet"} {
		if _, err := findNetwork(id); err == nil {
			t.Fatalf("unsupported network remains in catalog: %s", id)
		}
	}
	seen := map[string]bool{}
	for _, n := range networks {
		if seen[n.ID] || n.ID != n.Chain+"-"+n.Network || len(n.Products) == 0 {
			t.Fatalf("invalid network %+v", n)
		}
		seen[n.ID] = true
	}
	if _, err := filterNetworks("aptos", "indexer"); err == nil {
		t.Fatal("unsupported indexer product remains in catalog")
	}
	ns, err := filterNetworks("solana", "stream")
	if err != nil || len(ns) != 0 {
		t.Fatalf("%+v %v", ns, err)
	}
}

func TestCredentialsStayOutOfOutputAndConfig(t *testing.T) {
	a := newTestApp(t)
	secret := "sensitive-project-api-key"
	projectID, keyID := "project-1", "key-1"
	if err := a.keys.Set(projectCredentialKey(projectID, keyID), secret); err != nil {
		t.Fatal(err)
	}
	if err := a.config.update(context.Background(), func(c *config) error {
		c.Project = projectID
		c.ProjectKeys = map[string]string{projectID: keyID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rawConfig, err := os.ReadFile(a.config.path())
	if err != nil || strings.Contains(string(rawConfig), secret) {
		t.Fatal("key saved in config")
	}
	if err := a.saveSession(&session{"sensitive-access", "sensitive-refresh", time.Now().Add(time.Hour), a.env.Issuer, a.env.Resource}); err != nil {
		t.Fatal(err)
	}
	if c, out, e := run(t, a, "auth", "status", "-o", "json"); c != 0 || strings.Contains(out+e, "sensitive") {
		t.Fatalf("status failed or leaked: %d", c)
	}
	if c, _, e := run(t, a, "auth", "logout"); c != 0 {
		t.Fatal(e)
	}
	if _, err := a.keys.Get(sessionKey); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("session not deleted")
	}
	if _, err := a.keys.Get(projectCredentialKey(projectID, keyID)); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("project key not deleted")
	}
}

func TestAuthStatusLeadsSomewhereFromEveryEmptyState(t *testing.T) {
	a := newTestApp(t)
	c, out, e := run(t, a, "auth", "status")
	if c != 0 || e != "" {
		t.Fatalf("%d %s", c, e)
	}
	// Nothing is configured, so both halves have to name the command that configures them.
	for _, want := range []string{"not_logged_in", "Run nodit auth login.", "NODIT_API_KEY", "nodit project select"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	// A login that cannot renew itself asks for a new one instead of only reporting the expiry.
	if err := a.saveSession(&session{AccessToken: "a", ExpiresAt: time.Now().Add(-time.Hour), Issuer: a.env.Issuer, Resource: a.env.Resource}); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = run(t, a, "auth", "status"); !strings.Contains(out, "expired") || !strings.Contains(out, "Run nodit auth login.") {
		t.Fatalf("expired session without a way out: %s", out)
	}
	if _, err := a.accessToken(t.Context()); err == nil || !strings.Contains(err.Error(), "nodit auth login") {
		t.Fatalf("expired token error without a way out: %v", err)
	}
}

func TestAuthStatusLeadsSomewhereFromAHalfLinkedProject(t *testing.T) {
	for _, tc := range []struct{ name, keyID string }{{"never linked", ""}, {"missing from the store", testKeyID}} {
		a := newTestApp(t)
		a.getenv = func(string) string { return "" }
		if err := a.config.update(t.Context(), func(c *config) error {
			c.Project = "123"
			if tc.keyID != "" {
				c.ProjectKeys = map[string]string{"123": tc.keyID}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		_, out, _ := run(t, a, "auth", "status")
		if !strings.Contains(out, "nodit project select 123") {
			t.Fatalf("%s: %s", tc.name, out)
		}
	}
}

func TestADeadlineNamesTheFlagThatRaisesIt(t *testing.T) {
	a := newTestApp(t)
	a.getenv = func(name string) string {
		if name == "NODIT_API_KEY" {
			return "product-key"
		}
		return ""
	}
	// The transport fails while the deadline is already past, which is what a real timeout looks like
	// from here: the context error reaches the top untouched instead of an API failure.
	mockAPI(a, func(*http.Request) (*http.Response, error) { return nil, errors.New("transport gave up") })
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	a.stdout, a.stderr = &bytes.Buffer{}, &bytes.Buffer{}
	code := a.execute(ctx, []string{"data", "native", "balance", "--address", testAddress, "-n", "ethereum-mainnet"})
	e := a.stderr.(*bytes.Buffer).String()
	if code == 0 || !strings.Contains(e, "TIMEOUT") || !strings.Contains(e, "--timeout") {
		t.Fatalf("%d %s", code, e)
	}
}

func TestHeadlessAndUnavailableKeyring(t *testing.T) {
	a := newTestApp(t)
	if c, out, e := run(t, a, "auth", "login", "--no-interactive"); c != 1 || out != "" || !strings.Contains(e, "INTERACTION_REQUIRED") || !strings.Contains(e, "--no-interactive") {
		t.Fatalf("%d %s %s", c, out, e)
	}
	a.keys.(*fakeKeys).err = errors.New("keychain unavailable and sensitive diagnostics")
	a.getenv = func(key string) string {
		if key == "NODIT_AUTH_TOKEN" || key == "NODIT_API_KEY" {
			return "sensitive-env"
		}
		return ""
	}
	if c, out, e := run(t, a, "auth", "status"); c != 0 || strings.Contains(out+e, "sensitive") || !strings.Contains(out, "unverified") {
		t.Fatalf("%d %s", c, e)
	}
	if token, err := a.accessToken(context.Background()); err != nil || token != "sensitive-env" {
		t.Fatal("external credential did not take precedence")
	}
}

func TestCLIHelpErrorsAndFormats(t *testing.T) {
	a := newTestApp(t)
	a.keys.(*fakeKeys).err = errors.New("offline")
	for _, args := range [][]string{{"--help"}, {"version"}, {"network", "list"}, {"config", "path"}} {
		if c, out, e := run(t, a, args...); c != 0 || out == "" || e != "" {
			t.Fatalf("%v %d %s", args, c, e)
		}
	}
	for _, args := range [][]string{{"network", "get", "kaia-mainnet", "extra"}, {"network", "get", "no-network"}, {"auth", "key"}, {"--secret-unknown"}, {"version", "--timeout", "-1"}, {"version", "-o", "xml"}, {"version", "--server", "hidden"}} {
		if c, out, e := run(t, a, args...); c != 2 || out != "" || strings.Contains(e, "secret-") {
			t.Fatalf("%v %d %s %s", args, c, out, e)
		}
	}
	for _, format := range []string{"yaml", "json", "jsonl", "toon"} {
		if c, out, e := run(t, a, "network", "get", "solana-devnet", "-o", format); c != 0 || out == "" || e != "" {
			t.Fatalf("%s %d %s", format, c, e)
		}
	}
	// An unknown command still reports the error in the requested format.
	for _, args := range [][]string{{"no-such-command", "-o", "json"}, {"-o", "json", "no-such-command"}} {
		c, out, e := run(t, a, args...)
		if c != 2 || out != "" {
			t.Fatalf("%v %d %s", args, c, out)
		}
		var result struct {
			Error struct{ Code string } `json:"error"`
		}
		if json.Unmarshal([]byte(e), &result) != nil || result.Error.Code != "INVALID_ARGUMENT" {
			t.Fatalf("%v %s", args, e)
		}
	}
}

func TestVersionDoesNotExposeEndpoints(t *testing.T) {
	a := newTestApp(t)
	code, out, stderr := run(t, a, "version", "-o", "json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"version":`) {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	for _, value := range []string{a.env.Issuer, a.env.Resource, a.env.Domain} {
		if strings.Contains(out, value) {
			t.Fatalf("version exposed endpoint: %s", out)
		}
	}
}

func TestConfigConcurrentUpdates(t *testing.T) {
	a := newTestApp(t)
	errCh := make(chan error, 2)
	go func() {
		errCh <- a.config.update(context.Background(), func(c *config) error { c.Network = "ethereum-mainnet"; return nil })
	}()
	go func() {
		errCh <- a.config.update(context.Background(), func(c *config) error { c.Output = "toon"; return nil })
	}()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	c, err := a.config.read()
	if err != nil || c.Network != "ethereum-mainnet" || c.Output != "toon" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestEndpointIsolation(t *testing.T) {
	root := t.TempDir()
	for _, suffix := range []string{"alpha", "beta", "release"} {
		env := testEnvironment(t, suffix)
		s := configStore{filepath.Join(root, "nodit", env.Namespace)}
		if err := s.update(context.Background(), func(c *config) error { c.Output = "json"; return nil }); err != nil {
			t.Fatal(err)
		}
		a := newTestApp(t)
		a.env = env
		if suffix != "alpha" {
			other := testEnvironment(t, "alpha")
			b, _ := json.Marshal(session{"other-secret", "refresh", time.Now().Add(time.Hour), other.Issuer, other.Resource})
			_ = a.keys.Set(sessionKey, string(b))
			if _, err := a.loadSession(); err == nil {
				t.Fatal("cross-environment session accepted")
			}
		}
	}
}

func TestUnsupportedNetworkPointsAtRealIDs(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"ethereum", "ethereum-mainnet"},
		{"eth", "ethereum-mainnet"},
		{"mainnet", "-mainnet"},
		{"zzz", "nodit network list"},
		// A misspelling is not a prefix of anything, so only the edit distance can catch it.
		{"kaia-mainet", "kaia-mainnet"},
		{"etherem-mainnet", "ethereum-mainnet"},
		{"solana-devnett", "solana-devnet"},
		// Far enough from every id that naming one would be a guess.
		{"polygon-amoy", "nodit network list"},
	} {
		err := unsupportedNetwork(tc.id)
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: %v", tc.id, err)
		}
	}
	// Every suggestion has to exist in the catalog, and no answer may run long.
	for _, typed := range []string{"ethereum", "kaia-mainet", "mainnet", "sepolia"} {
		near := nearbyNetworks(typed)
		if len(near) > 3 {
			t.Fatalf("%s suggested %d networks", typed, len(near))
		}
		for _, id := range near {
			if _, err := findNetwork(id); err != nil {
				t.Fatalf("suggested a network that does not exist: %s", id)
			}
		}
	}
	// The closest id comes first so the likely fix is the one that is read.
	if near := nearbyNetworks("base-mainet"); len(near) == 0 || near[0] != "base-mainnet" {
		t.Fatalf("closest suggestion was %v", near)
	}
}

func TestMissingRequiredFlagsNameTheFlagAndCommand(t *testing.T) {
	a := newTestApp(t)
	code := a.execute(t.Context(), []string{"allowlist", "add", "1.2.3.4"})
	out := a.stderr.(*bytes.Buffer).String()
	for _, want := range []string{"--project", "--type", "nodit allowlist add --help", "nodit project list"} {
		if !strings.Contains(out, want) {
			t.Fatalf("exit %d, missing %q in %s", code, want, out)
		}
	}
}

func TestArgumentAndCommandErrorsPointSomewhere(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"rest", "GET"}, "Usage: nodit rest <method> <path>"},
		{[]string{"network", "get", "kaia-mainnet", "extra"}, "Usage: nodit network get <id>"},
		{[]string{"data", "toekn"}, "Did you mean token?"},
		{[]string{"webhook", "clasic"}, "Did you mean classic?"},
		{[]string{"netwrok"}, "Did you mean network?"},
		{[]string{"bogus"}, "Run nodit --help."},
		{[]string{"usage", "timeseries", "--interval", "1h"}, "Run nodit usage timeseries --help."},
		{[]string{"apikey", "get", "x", "--bogus"}, "Run nodit apikey get --help."},
		{[]string{"--bogus"}, "Run nodit --help."},
	} {
		a := newTestApp(t)
		a.execute(t.Context(), tc.args)
		if out := a.stderr.(*bytes.Buffer).String(); !strings.Contains(out, tc.want) {
			t.Fatalf("%v: missing %q in %s", tc.args, tc.want, out)
		}
	}
}

func TestBareLeafCommandAnswersWithHelp(t *testing.T) {
	for _, args := range [][]string{
		{"rpc"}, {"rest"}, {"network", "get"}, {"config", "set"}, {"apikey", "get"},
		{"project", "select"}, {"allowlist", "add"}, {"data", "entity", "lookup"},
		{"webhook", "classic", "delete"}, {"webhook", "classic", "addresses", "list"},
	} {
		a := newTestApp(t)
		code, out, e := run(t, a, args...)
		if code != 0 || e != "" {
			t.Fatalf("%v: exit %d, stderr %s", args, code, e)
		}
		for _, want := range []string{"Usage:", "Flags:"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v: missing %q in %s", args, want, out)
			}
		}
	}
}

func TestNetworkCompletionFollowsTheProduct(t *testing.T) {
	// stream reaches fewer networks than rpc.
	streamOnly, _ := suggest(networkIDs("stream"), "")
	for _, id := range streamOnly {
		n, err := findNetwork(id)
		if err != nil || !inWords(strings.Join(n.Products, " "), "stream") {
			t.Fatalf("%s is not a stream network", id)
		}
	}
	if got, _ := suggest(networkIDs("node"), "ethereum-"); len(got) == 0 {
		t.Fatal("prefix filtering dropped everything")
	}
}

func TestUsageNetworkFilterTakesEitherShape(t *testing.T) {
	for _, tc := range []struct{ protocol, network, wantProtocol, wantNetwork string }{
		{"", "ethereum-mainnet", "ethereum", "mainnet"},
		{"ethereum", "mainnet", "ethereum", "mainnet"},
		{"ethereum", "ethereum-mainnet", "ethereum", "mainnet"},
		{"ethereum", "", "ethereum", ""},
	} {
		p, n, err := splitNetworkFilter(tc.protocol, tc.network)
		if err != nil || p != tc.wantProtocol || n != tc.wantNetwork {
			t.Fatalf("%+v -> %s %s %v", tc, p, n, err)
		}
	}
	// A bare network half still needs its protocol, and a full ID must not contradict one.
	if _, _, err := splitNetworkFilter("", "mainnet"); err == nil {
		t.Fatal("bare network accepted without a protocol")
	}
	if _, _, err := splitNetworkFilter("polygon", "ethereum-mainnet"); err == nil {
		t.Fatal("contradicting protocol accepted")
	}
}

func TestDataHelpCarriesOnlyItsOwnRules(t *testing.T) {
	long := map[string]string{}
	for _, spec := range dataSpecs {
		long[spec.group+" "+spec.name] = dataLong(spec, []string{"ethereum-mainnet"})
	}
	for _, tc := range []struct{ command, absent string }{
		{"native balance", "--page"},
		{"native balance", "--number"},
		{"token metadata", "--page"},
		{"transaction get", "--page"},
		{"event by-type", "--address or --contract"},
	} {
		if strings.Contains(long[tc.command], tc.absent) {
			t.Fatalf("%s mentions %q, which does not apply to it", tc.command, tc.absent)
		}
	}
	for _, tc := range []struct{ command, present string }{
		{"block get", "--number"},
		{"token transfers", "--address or --contract"},
		{"token balances", "--page"},
	} {
		if !strings.Contains(long[tc.command], tc.present) {
			t.Fatalf("%s does not mention %q", tc.command, tc.present)
		}
	}
}

func TestShorthandsDoNotCollide(t *testing.T) {
	// cobra panics at registration on a duplicate shorthand, so walking the tree is the check.
	a := newTestApp(t)
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		seen := map[string]string{}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Shorthand == "" {
				return
			}
			if other, ok := seen[f.Shorthand]; ok {
				t.Fatalf("%s: -%s is both --%s and --%s", cmd.CommandPath(), f.Shorthand, other, f.Name)
			}
			seen[f.Shorthand] = f.Name
		})
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(a.command())
}

func TestFlagHelpShowsValuesNotFormatNames(t *testing.T) {
	a := newTestApp(t)
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Usage, "RFC3339") {
				t.Fatalf("%s --%s names a format instead of showing one: %s", cmd.CommandPath(), f.Name, f.Usage)
			}
		})
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(a.command())
}

func TestUsagePeriodDurationRejectsWhatItCannotRepresent(t *testing.T) {
	for _, tc := range []struct {
		period string
		want   time.Duration
	}{
		{"10m", 10 * time.Minute},
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"4w", 4 * 7 * 24 * time.Hour},
	} {
		got, err := usagePeriodDuration(tc.period)
		if err != nil || got != tc.want {
			t.Fatalf("%s -> %v %v", tc.period, got, err)
		}
	}
	// Multiplying these by the unit wraps, and a wrapped value reads as a window that was never asked
	// for: the first lands under the minimum, the second well over it.
	for _, period := range []string{"153722867281m", "10000000000000000m", "99999999999999999999m"} {
		if d, err := usagePeriodDuration(period); err == nil {
			t.Fatalf("%s accepted as %v", period, d)
		}
	}
	if _, err := usagePeriodDuration("30s"); err == nil {
		t.Fatal("an unknown unit was read as weeks")
	}
}
