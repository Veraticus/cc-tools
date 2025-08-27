package server

import (
	"context"
	"io"
)

// Runner executes a command with input and returns output.
// This is the core interface that all command runners implement.
type Runner interface {
	Run(ctx context.Context, input io.Reader) (io.Reader, error)
}

// LintRunner executes lint commands.
type LintRunner interface {
	Runner
}

// TestRunner executes test commands.
type TestRunner interface {
	Runner
}

// StatuslineGenerator generates statuslines from input.
type StatuslineGenerator interface {
	Generate(ctx context.Context, input io.Reader) (string, error)
}
