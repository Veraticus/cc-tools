package statusline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshsymonds/steward/internal/aliases"
)

func newTestResolver(t *testing.T, toml string) *aliases.Resolver {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "aliases.toml")
	if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := aliases.NewResolver(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newTestStatusline(t *testing.T, r *aliases.Resolver) *Statusline {
	t.Helper()
	return CreateStatusline(&Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: 240},
		Resolver:      r,
	})
}

func TestK8sChip_ARNStrippedAndProdColored(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createK8sComponent("arn:aws:eks:us-east-1:123456789012:cluster/prod-eks", 20)

	if !strings.Contains(comp.Text, "prod-eks") {
		t.Errorf("chip text should contain stripped name 'prod-eks', got %q", comp.Text)
	}
	if strings.Contains(comp.Text, "arn:aws") {
		t.Errorf("chip text should NOT contain the ARN prefix, got %q", comp.Text)
	}
	if comp.Color != "maroon" {
		t.Errorf("prod context should be maroon, got %q", comp.Color)
	}
}

func TestK8sChip_ConnectGatewayStripped(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	raw := "connectgateway_klover-loan-application_global_core-production-us-east1"
	comp := s.createK8sComponent(raw, 30)

	if !strings.Contains(comp.Text, "core-production-us-east1") {
		t.Errorf("chip text should contain stripped cluster, got %q", comp.Text)
	}
	if strings.Contains(comp.Text, "connectgateway") {
		t.Errorf("chip text should NOT contain 'connectgateway' prefix, got %q", comp.Text)
	}
	if comp.Color != "maroon" {
		t.Errorf("prod should be maroon, got %q", comp.Color)
	}
}

func TestK8sChip_AliasOverridesStrip(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, `
[k8s."connectgateway_klover-loan-application_global_core-production-us-east1"]
label = "klover prod"
env = "prod"
`))
	comp := s.createK8sComponent("connectgateway_klover-loan-application_global_core-production-us-east1", 20)

	if !strings.Contains(comp.Text, "klover prod") {
		t.Errorf("chip text should use alias label, got %q", comp.Text)
	}
	if comp.Color != "maroon" {
		t.Errorf("alias env=prod should yield maroon, got %q", comp.Color)
	}
}

func TestK8sChip_UnknownStaysTeal(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createK8sComponent("some-random-cluster", 20)

	if comp.Color != "teal" {
		t.Errorf("unknown env should be teal, got %q", comp.Color)
	}
}

func TestK8sChip_StagingYellow(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createK8sComponent("acme-staging-cluster", 25)
	if comp.Color != "yellow" {
		t.Errorf("staging should be yellow, got %q", comp.Color)
	}
}

func TestAwsChip_ProdRed(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createAwsComponent("acme-prod-admin", 20)
	if comp.Color != "red" {
		t.Errorf("prod profile should be red, got %q", comp.Color)
	}
}

func TestAwsChip_DevGreen(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createAwsComponent("acme-dev-poweruser", 20)
	if comp.Color != "green" {
		t.Errorf("dev profile should be green, got %q", comp.Color)
	}
}

func TestAwsChip_UnknownPeach(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createAwsComponent("personal", 20)
	if comp.Color != "peach" {
		t.Errorf("unknown profile should be peach, got %q", comp.Color)
	}
}

func TestAwsChip_AliasLabel(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, `
[aws."acme-prod-administrator"]
label = "acme prod"
env = "prod"
`))
	comp := s.createAwsComponent("acme-prod-administrator", 30)
	if !strings.Contains(comp.Text, "acme prod") {
		t.Errorf("chip should show alias label, got %q", comp.Text)
	}
	if comp.Color != "red" {
		t.Errorf("explicit env=prod should be red, got %q", comp.Color)
	}
}

func TestAwsProfileFromEnv_StripsExportPrefix(t *testing.T) {
	// The strip happens in the env-read boundary (awsProfileFromEnv),
	// not in createAwsComponent. This test pins that contract: a
	// misconfigured shell setting AWS_PROFILE to the literal value
	// "export AWS_PROFILE=foo" is normalized to just "foo" before
	// any chip rendering happens.
	er := NewMockEnvReader()
	er.vars["AWS_PROFILE"] = "export AWS_PROFILE=acme-prod-admin"
	got := awsProfileFromEnv(er)
	if got != "acme-prod-admin" {
		t.Errorf("awsProfileFromEnv should strip the export prefix; got %q", got)
	}

	// Plain values pass through unchanged.
	er.vars["AWS_PROFILE"] = "personal"
	if got = awsProfileFromEnv(er); got != "personal" {
		t.Errorf("plain value should pass through; got %q", got)
	}
}

func TestHostnameChip_UsesAlias(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, `
[hosts.ultraviolet]
label = "uv"
`))
	data := &CachedData{
		Hostname:  "ultraviolet",
		TermWidth: 240,
	}
	comps := s.collectRightComponents(data, "", componentMaxLengths{
		hostname: 20, branch: 25, aws: 20, k8s: 20, devspace: 15,
	})
	var hostComp *Component
	for i := range comps {
		if strings.Contains(comps[i].Text, "uv") {
			hostComp = &comps[i]
			break
		}
	}
	if hostComp == nil {
		t.Fatalf("expected hostname component with 'uv', got components: %+v", comps)
	}
	if hostComp.Color != "rosewater" {
		t.Errorf("host chip bg should be rosewater (no per-host tint), got %q", hostComp.Color)
	}
	if strings.Contains(hostComp.Text, "ultraviolet") {
		t.Errorf("hostname should be aliased to 'uv', got %q", hostComp.Text)
	}
}

func TestGcloudChip_ProdPink(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createGcloudComponent("acme-prod-project", 25)
	if comp.Color != "pink" {
		t.Errorf("prod gcloud should be pink, got %q", comp.Color)
	}
	if !strings.Contains(comp.Text, "acme-prod-project") {
		t.Errorf("chip text should contain project name, got %q", comp.Text)
	}
}

func TestGcloudChip_StagingMauve(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createGcloudComponent("acme-staging-project", 25)
	if comp.Color != "mauve" {
		t.Errorf("staging gcloud should be mauve, got %q", comp.Color)
	}
}

func TestGcloudChip_DevSapphire(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createGcloudComponent("acme-dev-project", 25)
	if comp.Color != "sapphire" {
		t.Errorf("dev gcloud should be sapphire, got %q", comp.Color)
	}
}

func TestGcloudChip_UnknownLavender(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	comp := s.createGcloudComponent("klover-loan-application", 25)
	if comp.Color != "lavender" {
		t.Errorf("unknown gcloud should be lavender, got %q", comp.Color)
	}
}

func TestGcloudChip_AliasLabelAndEnv(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, `
[gcloud."klover-loan-application"]
label = "klover"
env = "prod"
`))
	comp := s.createGcloudComponent("klover-loan-application", 25)
	if !strings.Contains(comp.Text, "klover") {
		t.Errorf("chip should show alias label, got %q", comp.Text)
	}
	if comp.Color != "pink" {
		t.Errorf("explicit env=prod should be pink, got %q", comp.Color)
	}
}

func TestGcloudChip_OrderBetweenAwsAndK8s(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{
		Hostname:      "host",
		GitBranch:     "main",
		GcloudProject: "my-project",
		K8sContext:    "my-cluster",
		TermWidth:     240,
	}
	comps := s.collectRightComponents(data, "my-aws-profile", componentMaxLengths{
		hostname: 20, branch: 25, aws: 20, gcloud: 20, k8s: 20, devspace: 15,
	})
	// Find AWS, gcloud, k8s chip indices.
	idxAws, idxGcloud, idxK8s := -1, -1, -1
	for i, c := range comps {
		switch {
		case strings.Contains(c.Text, "my-aws-profile"):
			idxAws = i
		case strings.Contains(c.Text, "my-project"):
			idxGcloud = i
		case strings.Contains(c.Text, "my-cluster"):
			idxK8s = i
		}
	}
	if idxAws < 0 || idxGcloud < 0 || idxK8s < 0 {
		t.Fatalf("expected aws, gcloud, and k8s chips; got order: %s", colorChain(comps))
	}
	if idxAws >= idxGcloud || idxGcloud >= idxK8s {
		t.Errorf("expected order aws(%d) < gcloud(%d) < k8s(%d); chain=%s",
			idxAws, idxGcloud, idxK8s, colorChain(comps))
	}
}

func TestHostnameChip_FallbackToRaw(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{Hostname: "vermissian", TermWidth: 240}
	comps := s.collectRightComponents(data, "", componentMaxLengths{
		hostname: 20, branch: 25, aws: 20, k8s: 20, devspace: 15,
	})
	found := false
	for _, c := range comps {
		if strings.Contains(c.Text, "vermissian") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hostname should fall back to raw 'vermissian', got %+v", comps)
	}
}
