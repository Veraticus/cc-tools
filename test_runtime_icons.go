package main

import (
	"fmt"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

func main() {
	// Can't access the constants directly since they're not exported
	// But we can check what the actual functions see
	fmt.Println("Testing by reading the icons.go file directly...")
	
	// The icons ARE in the file, so the issue must be elsewhere
	// Let's check if our Go build is handling them properly
	
	testStr := "" // leftChevron 
	fmt.Printf("Test leftChevron: bytes=%d runes=%d [%s]\n", 
		len([]byte(testStr)), len([]rune(testStr)), testStr)
}
