package statusline

import (
	"strings"
	"testing"

	"github.com/joshsymonds/steward/internal/aliases"
)

// TestAdjacencyInvariant_R9 enforces R9: across all combinations of
// present/absent right-side chips and all env states, no two adjacent
// chips share the same exact color.
//
// Chain: devspace | host | git | aws | gcloud | k8s.
// devspace and git are toggled present/absent (2x2). aws/gcloud/k8s
// each cycle through {absent, prod, staging, dev} (4x4x4). Host is
// always present. Total: 2 * 2 * 4 * 4 * 4 = 256 combinations.
func TestAdjacencyInvariant_R9(t *testing.T) {
	awsByEnv := map[aliases.Env]string{
		aliases.EnvUnknown: "",
		aliases.EnvProd:    "acme-prod-admin",
		aliases.EnvStaging: "acme-staging-admin",
		aliases.EnvDev:     "acme-dev-admin",
	}
	gcloudByEnv := map[aliases.Env]string{
		aliases.EnvUnknown: "",
		aliases.EnvProd:    "prod-project",
		aliases.EnvStaging: "staging-project",
		aliases.EnvDev:     "dev-project",
	}
	k8sByEnv := map[aliases.Env]string{
		aliases.EnvUnknown: "",
		aliases.EnvProd:    "prod-cluster",
		aliases.EnvStaging: "staging-cluster",
		aliases.EnvDev:     "dev-cluster",
	}

	envs := []aliases.Env{
		aliases.EnvUnknown,
		aliases.EnvProd,
		aliases.EnvStaging,
		aliases.EnvDev,
	}

	// One alias entry with explicit env so EnvUnknown means "absent",
	// not "value present but unclassified" — we need the chip itself
	// absent for the EnvUnknown row. We achieve that by using "" as
	// the raw value (collectRightComponents skips empty strings).

	for _, devspacePresent := range []bool{false, true} {
		for _, gitPresent := range []bool{false, true} {
			for _, awsEnv := range envs {
				for _, gcloudEnv := range envs {
					for _, k8sEnv := range envs {
						name := chainName(devspacePresent, gitPresent, awsEnv, gcloudEnv, k8sEnv)
						t.Run(name, func(t *testing.T) {
							s := newTestStatusline(t, newTestResolver(t, ""))
							data := &CachedData{
								Hostname:      "ultraviolet",
								TermWidth:     240,
								GcloudProject: gcloudByEnv[gcloudEnv],
								K8sContext:    k8sByEnv[k8sEnv],
							}
							if devspacePresent {
								data.Devspace = "mercury"
							}
							if gitPresent {
								data.GitBranch = "main"
							}

							comps := s.collectRightComponents(
								data,
								awsByEnv[awsEnv],
								componentMaxLengths{
									hostname: 20, branch: 25, aws: 20,
									gcloud: 20, k8s: 20, devspace: 15,
								},
							)
							assertNoAdjacentSameColor(t, comps)
						})
					}
				}
			}
		}
	}
}

func assertNoAdjacentSameColor(t *testing.T, comps []Component) {
	t.Helper()
	for i := 1; i < len(comps); i++ {
		if comps[i].Color == comps[i-1].Color {
			t.Errorf(
				"adjacent chips share color %q at positions %d-%d; chain colors=%s",
				comps[i].Color, i-1, i, colorChain(comps),
			)
		}
	}
}

func colorChain(comps []Component) string {
	parts := make([]string, len(comps))
	for i, c := range comps {
		parts[i] = c.Color
	}
	return "[" + strings.Join(parts, " | ") + "]"
}

func chainName(devspace, git bool, awsEnv, gcloudEnv, k8sEnv aliases.Env) string {
	var sb strings.Builder
	if devspace {
		sb.WriteString("dev_")
	}
	sb.WriteString("host_")
	if git {
		sb.WriteString("git_")
	}
	sb.WriteString("aws=")
	sb.WriteString(string(awsEnv))
	sb.WriteString("_gcloud=")
	sb.WriteString(string(gcloudEnv))
	sb.WriteString("_k8s=")
	sb.WriteString(string(k8sEnv))
	return sb.String()
}
