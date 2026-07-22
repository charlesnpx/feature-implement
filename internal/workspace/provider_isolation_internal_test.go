package workspace

import (
	"os"
	"strings"
	"testing"
)

func TestNonProviderProcessEnvironmentScrubsCredentialsAndDisablesGitCredentialPaths(t *testing.T) {
	safe, err := NewEnvironmentVariable("FEATURE_SAFE_VALUE", "present")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := BuildNonProviderProcessEnvironment([]string{
		"PATH=/usr/bin:/bin",
		"HOME=/tmp/non-provider-home",
		"FEATURE_SAFE_BASE=present",
		"GITHUB_APP_PRIVATE_KEY_BASE64=forbidden",
		"CUSTOM_PROVIDER_SIGNING_MATERIAL=forbidden",
		"KRB5CCNAME=/tmp/forbidden-credential-cache",
		"KRB5_CLIENT_KTNAME=/tmp/forbidden-keytab",
		"GITHUB_TOKEN=forbidden",
		"CI_JOB_TOKEN=forbidden",
		"AWS_ACCESS_KEY_ID=forbidden",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: forbidden",
	}, []EnvironmentVariable{safe})
	if err != nil {
		t.Fatal(err)
	}

	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("malformed environment entry %q", entry)
		}
		values[name] = value
	}
	for _, forbidden := range []string{
		"GITHUB_TOKEN", "CI_JOB_TOKEN", "AWS_ACCESS_KEY_ID", "SSH_AUTH_SOCK",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "FEATURE_SAFE_BASE",
		"GITHUB_APP_PRIVATE_KEY_BASE64", "CUSTOM_PROVIDER_SIGNING_MATERIAL",
		"KRB5CCNAME", "KRB5_CLIENT_KTNAME",
	} {
		if _, exists := values[forbidden]; exists {
			t.Fatalf("credential or redirect variable %s survived non-provider isolation", forbidden)
		}
	}
	for name, expected := range map[string]string{
		"PATH": "/usr/bin:/bin", "FEATURE_SAFE_VALUE": "present",
		"HOME": os.DevNull, "XDG_CONFIG_HOME": os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
		"GIT_CONFIG_SYSTEM": os.DevNull, "GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE": "Never", "GIT_ASKPASS": os.DevNull,
		"SSH_ASKPASS": os.DevNull, "GIT_SSH_COMMAND": os.DevNull,
	} {
		if values[name] != expected {
			t.Fatalf("isolated environment %s = %q, want %q", name, values[name], expected)
		}
	}

	credential, _ := NewEnvironmentVariable("GITHUB_TOKEN", "forbidden")
	if _, err := NewLocalAttemptGitAdapter("git", []EnvironmentVariable{credential}); err == nil {
		t.Fatal("local Git adapter accepted a provider credential")
	}
	argv, _ := NewArgv("go", "test", "./...")
	if _, err := NewCommand(argv, "/tmp", []EnvironmentVariable{credential}, ReplayNever); err == nil {
		t.Fatal("generic non-provider command accepted a provider credential")
	}
}

func TestTrustedGitArgumentsDisableHooksHelpersPromptsAndAmbientHeaders(t *testing.T) {
	arguments := trustedGitArguments("/tmp/repository", "status", "--short")
	joined := strings.Join(arguments, "\x00")
	for _, required := range []string{
		"core.hooksPath=" + os.DevNull,
		"credential.helper=",
		"credential.interactive=false",
		"core.askPass=" + os.DevNull,
		"http.extraHeader=",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("trusted Git arguments do not enforce %q: %#v", required, arguments)
		}
	}
}
