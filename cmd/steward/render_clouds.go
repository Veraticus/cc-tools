package main

import (
	"os"

	"github.com/joshsymonds/steward/internal/aliases"
	"github.com/joshsymonds/steward/internal/output"
	"github.com/joshsymonds/steward/internal/statusline"
)

// runRenderCloudsCommand prints the AWS/gcloud/k8s chip chain as raw
// ANSI for embedding in starship's right_format. Intended to be invoked
// from a starship custom module:
//
//	[custom.cloud_section]
//	when = "true"
//	command = "steward render-clouds"
//	format = "$output"
//
// Output always includes at least the closing right curve in sky (git's
// color), so the right side of the prompt seals correctly even when no
// cloud chips are present.
func runRenderCloudsCommand() {
	deps := &statusline.Dependencies{
		FileReader:    &statusline.DefaultFileReader{},
		CommandRunner: &statusline.DefaultCommandRunner{},
		EnvReader:     &statusline.DefaultEnvReader{},
		TerminalWidth: &statusline.DefaultTerminalWidth{},
		Resolver:      aliases.NewResolverFromDefaultPath(os.Stderr, "steward render-clouds"),
	}

	output.NewTerminal(os.Stdout, os.Stderr).Raw(statusline.RenderClouds(deps))
}
