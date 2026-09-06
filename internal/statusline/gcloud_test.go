package statusline

import (
	"os"
	"path/filepath"
	"testing"
)

func newGcloudDeps(t *testing.T, home string, override string) *Dependencies {
	t.Helper()
	envReader := NewMockEnvReader()
	envReader.vars["HOME"] = home
	if override != "" {
		envReader.vars["STEWARD_STATUSLINE_GCLOUD"] = override
	}
	return &Dependencies{
		FileReader:    &DefaultFileReader{},
		CommandRunner: &DefaultCommandRunner{},
		EnvReader:     envReader,
		TerminalWidth: &MockTerminalWidth{},
	}
}

func writeGcloudConfig(t *testing.T, home, activeConfig, content string) {
	t.Helper()
	gcloudDir := filepath.Join(home, ".config", "gcloud")
	if err := os.MkdirAll(gcloudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if activeConfig == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(gcloudDir, "active_config"), []byte(activeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	configsDir := filepath.Join(gcloudDir, "configurations")
	if err := os.MkdirAll(configsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(configsDir, "config_"+activeConfig)
	if err := os.WriteFile(cfgFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGetGcloudProject_HappyPath(t *testing.T) {
	home := t.TempDir()
	writeGcloudConfig(t, home, "default", "[core]\naccount = me@example.com\nproject = my-prod-project\n")

	s := CreateStatusline(newGcloudDeps(t, home, ""))
	got := s.getGcloudProject()
	if got != "my-prod-project" {
		t.Errorf("got %q, want my-prod-project", got)
	}
}

func TestGetGcloudProject_MissingActiveConfig(t *testing.T) {
	home := t.TempDir()
	s := CreateStatusline(newGcloudDeps(t, home, ""))
	if got := s.getGcloudProject(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestGetGcloudProject_NoProjectField(t *testing.T) {
	home := t.TempDir()
	writeGcloudConfig(t, home, "default", "[core]\naccount = me@example.com\n")

	s := CreateStatusline(newGcloudDeps(t, home, ""))
	if got := s.getGcloudProject(); got != "" {
		t.Errorf("got %q, want empty (no project field)", got)
	}
}

func TestGetGcloudProject_OverrideDevNull(t *testing.T) {
	home := t.TempDir()
	writeGcloudConfig(t, home, "default", "[core]\nproject = my-project\n")

	s := CreateStatusline(newGcloudDeps(t, home, "/dev/null"))
	if got := s.getGcloudProject(); got != "" {
		t.Errorf("got %q, want empty (override should suppress)", got)
	}
}

func TestGetGcloudProject_OldOverrideIgnored(t *testing.T) {
	home := t.TempDir()
	writeGcloudConfig(t, home, "default", "[core]\nproject = my-project\n")
	deps := newGcloudDeps(t, home, "")
	envReader, ok := deps.EnvReader.(*MockEnvReader)
	if !ok {
		t.Fatal("expected MockEnvReader")
	}
	envReader.vars["CLAUDE_STATUSLINE_GCLOUD"] = devNullOverride

	if got := CreateStatusline(deps).getGcloudProject(); got != "my-project" {
		t.Errorf("old override must be ignored, got %q", got)
	}
}

func TestGetGcloudProject_WhitespaceTolerant(t *testing.T) {
	home := t.TempDir()
	writeGcloudConfig(t, home, "work", "[core]\n   project   =    spaced-project   \n")

	s := CreateStatusline(newGcloudDeps(t, home, ""))
	if got := s.getGcloudProject(); got != "spaced-project" {
		t.Errorf("got %q, want spaced-project", got)
	}
}

func TestGetGcloudProject_QuotedValue(t *testing.T) {
	home := t.TempDir()
	writeGcloudConfig(t, home, "default", "[core]\nproject = \"quoted-project\"\n")

	s := CreateStatusline(newGcloudDeps(t, home, ""))
	if got := s.getGcloudProject(); got != "quoted-project" {
		t.Errorf("got %q, want quoted-project", got)
	}
}
