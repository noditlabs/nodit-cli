package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"runtime/debug"
	"strings"
)

// Set with -X at release time, empty in any other build.
var Version string

// Defaults so a build without linker flags still reaches the public service. A build for another
// deployment overrides all three.
var (
	AuthIssuer    = "https://auth.lambda256.io"
	APIResource   = "https://api.nodit.io"
	ProductDomain = "nodit.io"
)

// The tag linked in at release time, or the module version recorded for a build without one.
func ReportedVersion() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		// A build outside a module records "(devel)", which is not a version.
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "unreleased"
}

type Environment struct {
	Issuer    string `json:"issuer"`
	Resource  string `json:"resource"`
	Domain    string `json:"-"`
	Namespace string `json:"-"`
}

func Current() (Environment, error) {
	return NewEnvironment(AuthIssuer, APIResource, ProductDomain)
}

func NewEnvironment(issuer, resource, domain string) (Environment, error) {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	resource = strings.TrimSuffix(strings.TrimSpace(resource), "/")
	domain = strings.TrimSpace(domain)

	if err := validateBaseURL(issuer); err != nil {
		return Environment{}, fmt.Errorf("invalid OAuth issuer: %w", err)
	}
	if err := validateBaseURL(resource); err != nil {
		return Environment{}, fmt.Errorf("invalid API resource: %w", err)
	}
	if err := validateDomain(domain); err != nil {
		return Environment{}, fmt.Errorf("invalid product domain: %w", err)
	}
	sum := sha256.Sum256([]byte(issuer + "\x00" + resource + "\x00" + domain))
	return Environment{Issuer: issuer, Resource: resource, Domain: domain, Namespace: hex.EncodeToString(sum[:])}, nil
}

func validateBaseURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("must be an HTTPS origin")
	}
	return nil
}

func validateDomain(value string) error {
	u, err := url.Parse("https://" + value)
	if err != nil || value == "" || u.Host != value || u.Hostname() == "" || u.Port() != "" || u.Path != "" || !strings.Contains(u.Hostname(), ".") {
		return fmt.Errorf("must be a domain name without scheme, port, or path")
	}
	return nil
}
