package main

import (
    "fmt"
    "github.com/mattn/go-runewidth"
)

func main() {
    // Test various icons and their widths
    icons := []string{
        "",  // git icon
        " ",  // aws icon  
        "☸",  // k8s icon
        "",  // hostname icon
        "󰚩", "󱚝", "󱚟", "󱚡", "󱚣", "󱚥", // model icons
    }
    
    for _, icon := range icons {
        fmt.Printf("Icon: [%s] Bytes: %d Runes: %d Width: %d\n", 
            icon, len([]byte(icon)), len([]rune(icon)), runewidth.StringWidth(icon))
    }
    
    // Test the full git icon with spaces
    gitFull := "  "
    fmt.Printf("Git with spaces: [%s] Width: %d\n", gitFull, runewidth.StringWidth(gitFull))
}
