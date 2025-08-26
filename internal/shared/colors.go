// Package shared provides shared utilities for all cc-tools commands.
package shared

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// Raw ANSI escape codes matching the bash hooks.
const (
	ANSIRed    = "\033[0;31m"
	ANSIGreen  = "\033[0;32m"
	ANSIYellow = "\033[0;33m"
	ANSIBlue   = "\033[0;34m"
	ANSICyan   = "\033[0;36m"
	ANSIReset  = "\033[0m"
)

// RawANSIStyle provides raw ANSI formatting matching bash hooks exactly.
type RawANSIStyle struct {
	color string
}

// NewRawANSIStyle creates a raw ANSI style.
func NewRawANSIStyle(color string) RawANSIStyle {
	return RawANSIStyle{color: color}
}

// Render applies the ANSI color codes to text.
func (s RawANSIStyle) Render(text string) string {
	return fmt.Sprintf("%s%s%s", s.color, text, ANSIReset)
}

// Raw ANSI styles matching bash hooks exactly.
var (
	RawErrorStyle   = NewRawANSIStyle(ANSIRed)
	RawSuccessStyle = NewRawANSIStyle(ANSIGreen)
	RawWarningStyle = NewRawANSIStyle(ANSIYellow)
	RawInfoStyle    = NewRawANSIStyle(ANSIBlue)
	RawDebugStyle   = NewRawANSIStyle(ANSICyan)
)

// Standard color definitions.
var (
	Red    = lipgloss.Color("#f38ba8")
	Green  = lipgloss.Color("#a6e3a1")
	Yellow = lipgloss.Color("#f9e2af")
	Blue   = lipgloss.Color("#89dceb")
	Cyan   = lipgloss.Color("#94e2d5")
)

// Catppuccin Mocha colors for statusline.
var (
	Lavender  = lipgloss.Color("#b4befe")
	Mauve     = lipgloss.Color("#cba6f7")
	Rosewater = lipgloss.Color("#f5e0dc")
	Sky       = lipgloss.Color("#89dceb")
	Peach     = lipgloss.Color("#fab387")
	Teal      = lipgloss.Color("#94e2d5")
	Base      = lipgloss.Color("#1e1e2e")
)

// Lighter background variants for progress bar.
var (
	GreenLight  = lipgloss.Color("#567f51")
	YellowLight = lipgloss.Color("#95885f")
	PeachLight  = lipgloss.Color("#966b51")
	RedLight    = lipgloss.Color("#925364")
)

// Powerline characters.
const (
	LeftChevron  = ""
	LeftCurve    = ""
	RightCurve   = ""
	RightChevron = ""
)

// Progress bar characters.
const (
	ProgressLeftEmpty  = ""
	ProgressMidEmpty   = ""
	ProgressRightEmpty = ""
	ProgressLeftFull   = ""
	ProgressMidFull    = ""
	ProgressRightFull  = ""
)

// Styles for common output.
var (
	ErrorStyle   = lipgloss.NewStyle().Foreground(Red)
	SuccessStyle = lipgloss.NewStyle().Foreground(Green)
	WarningStyle = lipgloss.NewStyle().Foreground(Yellow)
	InfoStyle    = lipgloss.NewStyle().Foreground(Blue)
	DebugStyle   = lipgloss.NewStyle().Foreground(Cyan)
)

// Styles for statusline segments.
var (
	LavenderSegment  = lipgloss.NewStyle().Background(Lavender).Foreground(Base)
	SkySegment       = lipgloss.NewStyle().Background(Sky).Foreground(Base)
	MauveSegment     = lipgloss.NewStyle().Background(Mauve).Foreground(Base)
	RosewaterSegment = lipgloss.NewStyle().Background(Rosewater).Foreground(Base)
	PeachSegment     = lipgloss.NewStyle().Background(Peach).Foreground(Base)
	TealSegment      = lipgloss.NewStyle().Background(Teal).Foreground(Base)
)
