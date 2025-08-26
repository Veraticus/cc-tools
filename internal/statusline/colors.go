package statusline

import "fmt"

// CatppuccinMocha defines the Catppuccin Mocha color scheme using true color (24-bit) ANSI codes
type CatppuccinMocha struct{}

// Background colors with true color escape sequences
func (c CatppuccinMocha) LavenderBG() string  { return "\033[48;2;180;190;254m" } // #b4befe
func (c CatppuccinMocha) GreenBG() string     { return "\033[48;2;166;227;161m" } // #a6e3a1
func (c CatppuccinMocha) MauveBG() string     { return "\033[48;2;203;166;247m" } // #cba6f7
func (c CatppuccinMocha) RosewaterBG() string { return "\033[48;2;245;224;220m" } // #f5e0dc
func (c CatppuccinMocha) SkyBG() string       { return "\033[48;2;137;220;235m" } // #89dceb
func (c CatppuccinMocha) YellowBG() string    { return "\033[48;2;249;226;175m" } // #f9e2af
func (c CatppuccinMocha) PeachBG() string     { return "\033[48;2;250;179;135m" } // #fab387
func (c CatppuccinMocha) TealBG() string      { return "\033[48;2;148;226;213m" } // #94e2d5
func (c CatppuccinMocha) RedBG() string       { return "\033[48;2;243;139;168m" } // #f38ba8
func (c CatppuccinMocha) BaseBG() string      { return "\033[48;2;30;30;46m" }    // #1e1e2e

// Foreground colors
func (c CatppuccinMocha) LavenderFG() string  { return "\033[38;2;180;190;254m" } // #b4befe
func (c CatppuccinMocha) GreenFG() string     { return "\033[38;2;166;227;161m" } // #a6e3a1
func (c CatppuccinMocha) MauveFG() string     { return "\033[38;2;203;166;247m" } // #cba6f7
func (c CatppuccinMocha) RosewaterFG() string { return "\033[38;2;245;224;220m" } // #f5e0dc
func (c CatppuccinMocha) SkyFG() string       { return "\033[38;2;137;220;235m" } // #89dceb
func (c CatppuccinMocha) YellowFG() string    { return "\033[38;2;249;226;175m" } // #f9e2af
func (c CatppuccinMocha) PeachFG() string     { return "\033[38;2;250;179;135m" } // #fab387
func (c CatppuccinMocha) TealFG() string      { return "\033[38;2;148;226;213m" } // #94e2d5
func (c CatppuccinMocha) RedFG() string       { return "\033[38;2;243;139;168m" } // #f38ba8
func (c CatppuccinMocha) BaseFG() string      { return "\033[38;2;30;30;46m" }    // #1e1e2e (dark text on colored backgrounds)

// Lighter background variants for progress bar empty sections (muted versions of each color)
func (c CatppuccinMocha) GreenLightBG() string  { return "\033[48;2;86;127;81m" }  // Muted green
func (c CatppuccinMocha) YellowLightBG() string { return "\033[48;2;149;136;95m" } // Muted yellow
func (c CatppuccinMocha) PeachLightBG() string  { return "\033[48;2;150;107;81m" } // Muted peach
func (c CatppuccinMocha) RedLightBG() string    { return "\033[48;2;146;83;100m" } // Muted red

// Reset color
func (c CatppuccinMocha) NC() string { return "\033[0m" }

// Segment builds a colored segment with proper powerline formatting
func (c CatppuccinMocha) Segment(bg, fg, content string) string {
	return fmt.Sprintf("%s%s%s%s", bg, fg, content, c.NC())
}
