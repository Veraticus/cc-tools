// Package aliases provides shared label and environment classification
// for hostnames, AWS profiles, k8s contexts, and gcloud projects.
//
// The data lives in a single TOML file at a neutral, tool-agnostic
// location (see DefaultPath). Both the steward statusline and the
// starship prompt resolve display labels through this package so the
// two lines stay in sync.
package aliases

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Kind selects which alias section to look up in.
type Kind int

const (
	// KindHost looks up under [hosts.*].
	KindHost Kind = iota
	// KindAWS looks up under [aws."*"].
	KindAWS
	// KindK8s looks up under [k8s."*"] and applies prefix-strip regexes.
	KindK8s
	// KindGcloud looks up under [gcloud."*"].
	KindGcloud
)

// Env represents the classified environment.
type Env string

const (
	// EnvUnknown is the default when no pattern matches.
	EnvUnknown Env = "unknown"
	// EnvProd matches prod/production patterns.
	EnvProd Env = "prod"
	// EnvStaging matches staging patterns.
	EnvStaging Env = "staging"
	// EnvDev matches dev/sandbox/test patterns.
	EnvDev Env = "dev"
)

// Resolver looks up labels and classifies environments from an alias table.
// A nil internal table means "no file loaded" — Resolve still works via
// built-in regex strips and default env patterns.
type Resolver struct {
	table *table
}

type table struct {
	Hosts       map[string]entry    `toml:"hosts"`
	AWS         map[string]entry    `toml:"aws"`
	K8s         map[string]entry    `toml:"k8s"`
	Gcloud      map[string]entry    `toml:"gcloud"`
	EnvPatterns map[string][]string `toml:"env_patterns"`
}

type entry struct {
	Label string `toml:"label"`
	Env   string `toml:"env"`
}

// DefaultPath returns the conventional location of the alias file.
// $STATUSLINE_ALIASES overrides everything. Otherwise:
// $XDG_CONFIG_HOME/statusline-aliases/aliases.toml,
// falling back to $HOME/.config/statusline-aliases/aliases.toml.
func DefaultPath() string {
	if p := os.Getenv("STATUSLINE_ALIASES"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "statusline-aliases", "aliases.toml")
}

// NewResolverFromDefaultPath builds a Resolver for the alias file at
// DefaultPath. Designed for the statusline render paths that want
// best-effort loading:
//   - missing file: emits one stderr line ("X: PATH missing, using
//     built-in patterns") and returns a no-table Resolver
//   - parse error: emits one stderr line and returns a no-table Resolver
//   - success: returns the parsed Resolver
//
// stderr is the destination for diagnostic output (typically os.Stderr);
// prefix is the binary name used in messages so callers like steward,
// steward-statusline, and render-clouds are distinguishable in logs.
//
// Use this from rendering paths only. Callers that need to fail loudly
// on parse errors (e.g. the `steward resolve` subcommand) should call
// NewResolver directly.
func NewResolverFromDefaultPath(stderr io.Writer, prefix string) *Resolver {
	path := DefaultPath()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		_, _ = fmt.Fprintf(stderr, "%s: %s missing, using built-in patterns\n", prefix, path)
	}
	r, err := NewResolver(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: alias file parse error: %v\n", prefix, err)
		r, _ = NewResolver("")
	}
	return r
}

// NewResolver loads the alias table from path. A missing file is not an
// error — the returned Resolver still works using built-in patterns.
// Parse errors ARE returned.
func NewResolver(path string) (*Resolver, error) {
	var t table
	if _, err := toml.DecodeFile(path, &t); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Resolver{table: nil}, nil
		}
		return nil, fmt.Errorf("decoding alias file %s: %w", path, err)
	}
	return &Resolver{table: &t}, nil
}

// Resolve returns the display label and classified environment for raw.
//
// Lookup order:
//  1. Explicit entry in [kind."raw"] → use entry.Label (or raw if Label empty);
//     env = entry.Env if set, else classify(raw).
//  2. For KindK8s, apply ARN / GKE / connectgateway prefix-strip regexes.
//  3. Else use raw as the label.
//  4. Classify env via env_patterns substring match on the ORIGINAL raw value.
func (r *Resolver) Resolve(kind Kind, raw string) (string, Env) {
	if raw == "" {
		return "", EnvUnknown
	}

	if label, env, ok := r.resolveExplicit(kind, raw); ok {
		return label, env
	}

	label := raw
	if kind == KindK8s {
		label = stripK8sPrefix(raw)
	}
	return label, r.classify(raw)
}

// resolveExplicit looks up an explicit [kind."raw"] entry. It returns the
// entry's label (or raw if Label is empty) and env (entry.Env if set, else
// classify(raw)), plus whether an entry was found at all.
func (r *Resolver) resolveExplicit(kind Kind, raw string) (string, Env, bool) {
	if r.table == nil {
		return "", EnvUnknown, false
	}
	e, ok := r.entriesFor(kind)[raw]
	if !ok {
		return "", EnvUnknown, false
	}
	label := e.Label
	if label == "" {
		label = raw
	}
	if e.Env != "" {
		return label, parseEnv(e.Env), true
	}
	return label, r.classify(raw), true
}

func (r *Resolver) entriesFor(kind Kind) map[string]entry {
	if r.table == nil {
		return nil
	}
	switch kind {
	case KindHost:
		return r.table.Hosts
	case KindAWS:
		return r.table.AWS
	case KindK8s:
		return r.table.K8s
	case KindGcloud:
		return r.table.Gcloud
	}
	return nil
}

func defaultPatterns() map[string][]string {
	return map[string][]string{
		string(EnvProd):    {"prod", "production"},
		string(EnvStaging): {"stag", "staging"},
		string(EnvDev):     {"dev", "sandbox", "sbx", "test"},
	}
}

func (r *Resolver) envPatterns() map[string][]string {
	if r.table != nil && len(r.table.EnvPatterns) > 0 {
		return r.table.EnvPatterns
	}
	return defaultPatterns()
}

// classify checks raw against env_patterns. Prod beats staging beats dev
// when multiple patterns match — this matches the "principle of louder
// signal wins" so a literal "prod-staging-mixed" classifies as prod.
func (r *Resolver) classify(raw string) Env {
	patterns := r.envPatterns()
	lower := strings.ToLower(raw)
	for _, env := range []Env{EnvProd, EnvStaging, EnvDev} {
		for _, p := range patterns[string(env)] {
			if strings.Contains(lower, strings.ToLower(p)) {
				return env
			}
		}
	}
	return EnvUnknown
}

var (
	eksARNRegex    = regexp.MustCompile(`^arn:aws:eks:[^:]+:[^:]+:cluster/(.+)$`)
	gkeRegex       = regexp.MustCompile(`^gke_[^_]+_[^_]+_(.+)$`)
	connectGwRegex = regexp.MustCompile(`^connectgateway_[^_]+_global_(.+)$`)
)

func stripK8sPrefix(raw string) string {
	if m := eksARNRegex.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if m := gkeRegex.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if m := connectGwRegex.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}

func parseEnv(s string) Env {
	switch strings.ToLower(s) {
	case "prod", "production":
		return EnvProd
	case "stag", "staging":
		return EnvStaging
	case "dev", "development", "sandbox", "test":
		return EnvDev
	default:
		return EnvUnknown
	}
}
