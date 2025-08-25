package statusline

import (
	"context"
	"fmt"
	"os/exec"
)

// CommandRunner executes external commands.
type CommandRunner interface {
	RunContext(ctx context.Context, name string, args ...string) error
	OutputContext(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Dependencies holds all external dependencies for the statusline package.
type Dependencies struct {
	Runner CommandRunner
}

// realCommandRunner is the production implementation of CommandRunner.
type realCommandRunner struct{}

func (r *realCommandRunner) RunContext(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run command %s: %w", name, err)
	}
	return nil
}

func (r *realCommandRunner) OutputContext(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("get command output %s: %w", name, err)
	}
	return output, nil
}

// NewDefaultDependencies creates production dependencies.
func NewDefaultDependencies() *Dependencies {
	return &Dependencies{
		Runner: &realCommandRunner{},
	}
}
