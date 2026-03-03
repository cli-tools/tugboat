package repo

import (
	"os"
	"strings"
	"testing"
)

func TestGitEnvWithAuth(t *testing.T) {
	t.Run("with token sets credential helper", func(t *testing.T) {
		env := gitEnvWithAuth("mytoken123")
		var hasPrompt, hasCount, hasKey, hasValue bool
		for _, e := range env {
			switch {
			case e == "GIT_TERMINAL_PROMPT=0":
				hasPrompt = true
			case e == "GIT_CONFIG_COUNT=1":
				hasCount = true
			case e == "GIT_CONFIG_KEY_0=credential.helper":
				hasKey = true
			case strings.HasPrefix(e, "GIT_CONFIG_VALUE_0=") && strings.Contains(e, "mytoken123"):
				hasValue = true
			}
		}
		if !hasPrompt {
			t.Error("missing GIT_TERMINAL_PROMPT=0")
		}
		if !hasCount {
			t.Error("missing GIT_CONFIG_COUNT=1")
		}
		if !hasKey {
			t.Error("missing GIT_CONFIG_KEY_0=credential.helper")
		}
		if !hasValue {
			t.Error("missing GIT_CONFIG_VALUE_0 with token")
		}
	})

	t.Run("without token has no credential config", func(t *testing.T) {
		env := gitEnvWithAuth("")
		for _, e := range env {
			if strings.HasPrefix(e, "GIT_CONFIG_COUNT") {
				t.Errorf("unexpected GIT_CONFIG_COUNT in env: %s", e)
			}
		}
	})

}

func TestGitEnvNoPrompt(t *testing.T) {
	env := gitEnvNoPrompt()
	found := false
	for _, e := range env {
		if e == "GIT_TERMINAL_PROMPT=0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("gitEnvNoPrompt() missing GIT_TERMINAL_PROMPT=0")
	}

	// Should include existing env vars.
	path := os.Getenv("PATH")
	if path != "" {
		foundPath := false
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				foundPath = true
				break
			}
		}
		if !foundPath {
			t.Error("gitEnvNoPrompt() missing inherited PATH")
		}
	}
}
