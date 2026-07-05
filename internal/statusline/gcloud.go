package statusline

import (
	"path/filepath"
	"strings"
)

// getGcloudProject returns the active gcloud project, or "" if none.
//
// Reads ~/.config/gcloud/active_config (a single-line file naming the
// active configuration), then ~/.config/gcloud/configurations/config_<name>
// (an INI file), then extracts `project` from the [core] section.
//
// CLAUDE_STATUSLINE_GCLOUD=/dev/null suppresses the chip entirely;
// useful for tests and for users who don't want gcloud state shown.
func (s *Statusline) getGcloudProject() string {
	if override := s.deps.EnvReader.Get("CLAUDE_STATUSLINE_GCLOUD"); override == devNullOverride {
		return ""
	}

	home := s.deps.EnvReader.Get("HOME")
	if home == "" {
		return ""
	}

	gcloudDir := filepath.Join(home, ".config", "gcloud")
	activeBytes, err := s.deps.FileReader.ReadFile(filepath.Join(gcloudDir, "active_config"))
	if err != nil || len(activeBytes) == 0 {
		return ""
	}
	active := strings.TrimSpace(string(activeBytes))
	if active == "" {
		return ""
	}

	configBytes, err := s.deps.FileReader.ReadFile(filepath.Join(gcloudDir, "configurations", "config_"+active))
	if err != nil {
		return ""
	}

	return extractGcloudProject(string(configBytes))
}

// extractGcloudProject parses the [core] section of a gcloud config INI
// and returns the project value, or "" if missing. Section header lookup
// is case-insensitive; key match is exact ("project").
func extractGcloudProject(content string) string {
	inCore := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section := strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			inCore = section == "core"
			continue
		}
		if !inCore {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		if key != "project" {
			continue
		}
		value := strings.TrimSpace(trimmed[eq+1:])
		value = strings.Trim(value, `"'`)
		return value
	}
	return ""
}
