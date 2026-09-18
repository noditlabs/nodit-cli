package buildinfo

import "testing"

func TestNewEnvironment(t *testing.T) {
	env, err := NewEnvironment("https://auth.example.test/", "https://api.example.test/", "services.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if env.Issuer != "https://auth.example.test" || env.Resource != "https://api.example.test" || env.Domain != "services.example.test" || len(env.Namespace) != 64 {
		t.Fatalf("unexpected environment: %+v", env)
	}
	again, _ := NewEnvironment(env.Issuer, env.Resource, env.Domain)
	other, _ := NewEnvironment(env.Issuer, env.Resource, "other.example.test")
	if again.Namespace != env.Namespace || other.Namespace == env.Namespace {
		t.Fatal("endpoint namespace is not stable and isolated")
	}
}

func TestNewEnvironmentRejectsInvalidBuildParameters(t *testing.T) {
	tests := []struct {
		issuer, resource, domain string
	}{
		{"http://auth.example.test", "https://api.example.test", "services.example.test"},
		{"https://auth.example.test/path", "https://api.example.test", "services.example.test"},
		{"https://auth.example.test", "", "services.example.test"},
		{"https://auth.example.test", "https://api.example.test", "https://services.example.test"},
		{"https://auth.example.test", "https://api.example.test", "localhost"},
	}
	for _, tc := range tests {
		if _, err := NewEnvironment(tc.issuer, tc.resource, tc.domain); err == nil {
			t.Fatalf("accepted invalid parameters: %#v", tc)
		}
	}
}

func TestReportedVersionPrefersTheInjectedTag(t *testing.T) {
	previous := Version
	t.Cleanup(func() { Version = previous })

	Version = "v1.2.3"
	if got := ReportedVersion(); got != "v1.2.3" {
		t.Fatalf("ReportedVersion() = %q, want the injected tag", got)
	}

	// A test binary has no module version behind it.
	Version = ""
	if got := ReportedVersion(); got != "unreleased" {
		t.Fatalf("ReportedVersion() = %q, want unreleased", got)
	}
}

func TestDefaultBuildIsUsable(t *testing.T) {
	// A build with no linker flags has only the defaults.
	env, err := Current()
	if err != nil {
		t.Fatalf("default build rejected: %v", err)
	}
	if env.Issuer == "" || env.Resource == "" || env.Domain == "" {
		t.Fatalf("default environment is incomplete: %#v", env)
	}
}

func TestCurrentRequiresInjectedValues(t *testing.T) {
	previous := []string{AuthIssuer, APIResource, ProductDomain}
	t.Cleanup(func() {
		AuthIssuer, APIResource, ProductDomain = previous[0], previous[1], previous[2]
	})
	AuthIssuer, APIResource, ProductDomain = "", "", ""
	if _, err := Current(); err == nil {
		t.Fatal("unconfigured build accepted")
	}
}
