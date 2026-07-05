package statusline

import "github.com/Veraticus/cc-tools/internal/aliases"

// Catppuccin Mocha color names, used as Component.Color / narrowChip.Color
// values and as the lookup keys for getColorBG / getColorFG.
const (
	colorRed       = "red"
	colorGreen     = "green"
	colorPeach     = "peach"
	colorPink      = "pink"
	colorMauve     = "mauve"
	colorSapphire  = "sapphire"
	colorLavender  = "lavender"
	colorMaroon    = "maroon"
	colorYellow    = "yellow"
	colorTeal      = "teal"
	colorRosewater = "rosewater"
	// colorOverlay is the git chip's draft-PR glyph color: Catppuccin
	// Mocha's overlay0, a muted gray. Not used as a chip background —
	// only as an inline foreground override inside the git chip body
	// (see createGitComponent) — so it deliberately doesn't appear in
	// awsBgColor/gcloudBgColor/k8sBgColor above.
	colorOverlay = "overlay"
)

// awsBgColor returns the chip background color name for an AWS profile's env.
// Matches epic R4: peach (unknown), red (prod), peach (staging), green (dev).
// Staging shares peach with unknown — the chip text still distinguishes them
// and adjacency invariants tolerate the reuse.
func awsBgColor(env aliases.Env) string {
	switch env {
	case aliases.EnvProd:
		return colorRed
	case aliases.EnvDev:
		return colorGreen
	case aliases.EnvStaging, aliases.EnvUnknown:
		return colorPeach
	default:
		return colorPeach
	}
}

// gcloudBgColor returns the chip background color name for a gcloud project's env.
// Matches epic R3: lavender (unknown), pink (prod), mauve (staging), sapphire (dev).
// These colors are reused from other chip positions but the adjacency invariant
// (R9) keeps them safe: gcloud sits between aws and k8s, neither of which uses
// these values.
func gcloudBgColor(env aliases.Env) string {
	switch env {
	case aliases.EnvProd:
		return colorPink
	case aliases.EnvStaging:
		return colorMauve
	case aliases.EnvDev:
		return colorSapphire
	case aliases.EnvUnknown:
		return colorLavender
	default:
		return colorLavender
	}
}

// k8sBgColor returns the chip background color name for a k8s context's env.
// Matches epic R4: teal (unknown), maroon (prod), yellow (staging), teal (dev).
// Dev shares teal with unknown by design.
func k8sBgColor(env aliases.Env) string {
	switch env {
	case aliases.EnvProd:
		return colorMaroon
	case aliases.EnvStaging:
		return colorYellow
	case aliases.EnvDev, aliases.EnvUnknown:
		return colorTeal
	default:
		return colorTeal
	}
}
