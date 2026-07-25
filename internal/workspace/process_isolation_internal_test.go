package workspace

import (
	"os"
	"strings"
	"testing"
)

func TestIsolatedProcessEnvironmentScrubsSensitiveStateAndDisablesGitAuthenticationPaths(
	t *testing.T,
) {
	safe, err := NewEnvironmentVariable("FEATURE_SAFE_VALUE", "present")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := BuildIsolatedProcessEnvironment([]string{
		"PATH=/usr/bin:/bin",
		"HOME=/tmp/ambient-home",
		"FEATURE_SAFE_BASE=present",
		"SERVICE_TOKEN=forbidden",
		"SERVICE_CONFIG_DIR=/tmp/forbidden-config",
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
		"FEATURE_SAFE_BASE",
		"SERVICE_TOKEN",
		"SERVICE_CONFIG_DIR",
		"SSH_AUTH_SOCK",
		"GIT_CONFIG_COUNT",
		"GIT_CONFIG_KEY_0",
		"GIT_CONFIG_VALUE_0",
	} {
		if _, exists := values[forbidden]; exists {
			t.Fatalf("sensitive or redirect variable %s survived isolation", forbidden)
		}
	}
	for name, expected := range map[string]string{
		"PATH":                "/usr/bin:/bin",
		"FEATURE_SAFE_VALUE":  "present",
		"HOME":                os.DevNull,
		"XDG_CONFIG_HOME":     os.DevNull,
		"GIT_ATTR_NOSYSTEM":   "1",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE":     "Never",
		"GIT_ASKPASS":         os.DevNull,
		"SSH_ASKPASS":         os.DevNull,
		"GIT_SSH_COMMAND":     os.DevNull,
	} {
		if values[name] != expected {
			t.Fatalf("isolated environment %s = %q, want %q", name, values[name], expected)
		}
	}

	for _, name := range []string{"SERVICE_TOKEN", "SERVICE_CONFIG_DIR"} {
		sensitive, variableErr := NewEnvironmentVariable(name, "forbidden")
		if variableErr != nil {
			t.Fatal(variableErr)
		}
		if _, adapterErr := NewLocalAttemptGitAdapter(
			"git", []EnvironmentVariable{sensitive},
		); adapterErr == nil {
			t.Fatalf("local Git adapter accepted sensitive variable %s", name)
		}
		argv, argvErr := NewArgv("go", "test", "./...")
		if argvErr != nil {
			t.Fatal(argvErr)
		}
		if _, commandErr := NewCommand(
			argv, "/tmp", []EnvironmentVariable{sensitive}, ReplayNever,
		); commandErr == nil {
			t.Fatalf("local command accepted sensitive variable %s", name)
		}
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
		"log.showSignature=false",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("trusted Git arguments do not enforce %q: %#v", required, arguments)
		}
	}
}
