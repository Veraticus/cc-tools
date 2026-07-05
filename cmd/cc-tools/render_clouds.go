package main

import (
	"os"

	"github.com/Veraticus/cc-tools/internal/aliases"
	"github.com/Veraticus/cc-tools/internal/output"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

// runRenderCloudsCommand prints the AWS/gcloud/k8s chip chain as raw
// ANSI for embedding in starship's right_format. Intended to be invoked
// from a starship custom module:
//
//	[custom.cloud_section]
//	when = "true"
//	command = "cc-tools render-clouds"
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
		Resolver:      aliases.NewResolverFromDefaultPath(os.Stderr, "cc-tools render-clouds"),
	}

	output.NewTerminal(os.Stdout, os.Stderr).Raw(statusline.RenderClouds(deps))
}
