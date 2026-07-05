package statusline

// CatppuccinMocha defines the Catppuccin Mocha color scheme using true color (24-bit) ANSI codes.
type CatppuccinMocha struct{}

// LavenderBG returns lavender background color.
func (c CatppuccinMocha) LavenderBG() string { return "\033[48;2;180;190;254m" } // #b4befe

// GreenBG returns green background color.
func (c CatppuccinMocha) GreenBG() string { return "\033[48;2;166;227;161m" } // #a6e3a1

// MauveBG returns mauve background color.
func (c CatppuccinMocha) MauveBG() string { return "\033[48;2;203;166;247m" } // #cba6f7

// RosewaterBG returns rosewater background color.
func (c CatppuccinMocha) RosewaterBG() string { return "\033[48;2;245;224;220m" } // #f5e0dc

// SkyBG returns sky background color.
func (c CatppuccinMocha) SkyBG() string { return "\033[48;2;137;220;235m" } // #89dceb

// YellowBG returns yellow background color.
func (c CatppuccinMocha) YellowBG() string { return "\033[48;2;249;226;175m" } // #f9e2af

// PeachBG returns peach background color.
func (c CatppuccinMocha) PeachBG() string { return "\033[48;2;250;179;135m" } // #fab387

// TealBG returns teal background color.
func (c CatppuccinMocha) TealBG() string { return "\033[48;2;148;226;213m" } // #94e2d5

// RedBG returns red background color.
func (c CatppuccinMocha) RedBG() string { return "\033[48;2;243;139;168m" } // #f38ba8

// MaroonBG returns maroon background color.
func (c CatppuccinMocha) MaroonBG() string { return "\033[48;2;235;160;172m" } // #eba0ac

// PinkBG returns pink background color.
func (c CatppuccinMocha) PinkBG() string { return "\033[48;2;245;194;231m" } // #f5c2e7

// SapphireBG returns sapphire background color.
func (c CatppuccinMocha) SapphireBG() string { return "\033[48;2;116;199;236m" } // #74c7ec

// LavenderFG returns lavender foreground color.
func (c CatppuccinMocha) LavenderFG() string { return "\033[38;2;180;190;254m" } // #b4befe

// GreenFG returns green foreground color.
func (c CatppuccinMocha) GreenFG() string { return "\033[38;2;166;227;161m" } // #a6e3a1

// MauveFG returns mauve foreground color.
func (c CatppuccinMocha) MauveFG() string { return "\033[38;2;203;166;247m" } // #cba6f7

// RosewaterFG returns rosewater foreground color.
func (c CatppuccinMocha) RosewaterFG() string { return "\033[38;2;245;224;220m" } // #f5e0dc

// SkyFG returns sky foreground color.
func (c CatppuccinMocha) SkyFG() string { return "\033[38;2;137;220;235m" } // #89dceb

// YellowFG returns yellow foreground color.
func (c CatppuccinMocha) YellowFG() string { return "\033[38;2;249;226;175m" } // #f9e2af

// PeachFG returns peach foreground color.
func (c CatppuccinMocha) PeachFG() string { return "\033[38;2;250;179;135m" } // #fab387

// TealFG returns teal foreground color.
func (c CatppuccinMocha) TealFG() string { return "\033[38;2;148;226;213m" } // #94e2d5

// RedFG returns red foreground color.
func (c CatppuccinMocha) RedFG() string { return "\033[38;2;243;139;168m" } // #f38ba8

// MaroonFG returns maroon foreground color.
func (c CatppuccinMocha) MaroonFG() string { return "\033[38;2;235;160;172m" } // #eba0ac

// PinkFG returns pink foreground color.
func (c CatppuccinMocha) PinkFG() string { return "\033[38;2;245;194;231m" } // #f5c2e7

// SapphireFG returns sapphire foreground color.
func (c CatppuccinMocha) SapphireFG() string { return "\033[38;2;116;199;236m" } // #74c7ec

// BaseFG returns base foreground color (dark text on colored backgrounds).
func (c CatppuccinMocha) BaseFG() string { return "\033[38;2;30;30;46m" } // #1e1e2e

// TealLightBG returns light teal background color for progress bar.
func (c CatppuccinMocha) TealLightBG() string { return "\033[48;2;89;136;128m" } // Muted teal

// YellowLightBG returns light yellow background color for progress bar.
func (c CatppuccinMocha) YellowLightBG() string { return "\033[48;2;149;136;95m" } // Muted yellow

// RedLightBG returns light red background color for progress bar.
func (c CatppuccinMocha) RedLightBG() string { return "\033[48;2;146;83;100m" } // Muted red

// NC returns the ANSI reset sequence to clear all color formatting.
func (c CatppuccinMocha) NC() string { return "\033[0m" }
