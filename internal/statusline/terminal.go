package statusline

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// DefaultTerminalWidth provides terminal width detection
type DefaultTerminalWidth struct{}

// GetWidth returns the current terminal width
func (t *DefaultTerminalWidth) GetWidth() int {
	// Try environment variable first
	if columns := os.Getenv("COLUMNS"); columns != "" {
		if width, err := strconv.Atoi(columns); err == nil && width > 0 {
			return width
		}
	}
	
	// Try ioctl
	if width := getTerminalWidthIoctl(); width > 0 {
		return width
	}
	
	// Try tput command
	if width := getTerminalWidthTput(); width > 0 {
		return width
	}
	
	// Default fallback
	return 210
}

func getTerminalWidthIoctl() int {
	type winsize struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}
	
	ws := &winsize{}
	retCode, _, _ := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdout),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)))
	
	if int(retCode) == 0 {
		return int(ws.Col)
	}
	return 0
}

func getTerminalWidthTput() int {
	cmd := exec.Command("tput", "cols")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	
	width, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0
	}
	
	return width
}