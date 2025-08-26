package main

import (
	"fmt"
	"github.com/mattn/go-runewidth"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

func main() {
	fmt.Println("Testing icon display widths:")
	fmt.Println("=============================")
	
	// Powerline characters
	fmt.Println("\nPowerline characters:")
	testIcon("LeftChevron", statusline.LeftChevron)
	testIcon("LeftCurve", statusline.LeftCurve)
	testIcon("RightCurve", statusline.RightCurve)
	testIcon("RightChevron", statusline.RightChevron)
	
	// Section icons
	fmt.Println("\nSection icons:")
	testIcon("GitIcon", statusline.GitIcon)
	testIcon("AwsIcon", statusline.AwsIcon)
	testIcon("K8sIcon", statusline.K8sIcon)
	testIcon("HostnameIcon", statusline.HostnameIcon)
	testIcon("ContextIcon", statusline.ContextIcon)
	
	// Model icons (one at a time)
	fmt.Println("\nModel icons:")
	for i, r := range statusline.ModelIcons {
		testIcon(fmt.Sprintf("ModelIcon[%d]", i), string(r))
	}
	
	// Progress bar characters
	fmt.Println("\nProgress bar characters:")
	testIcon("ProgressLeftEmpty", statusline.ProgressLeftEmpty)
	testIcon("ProgressMidEmpty", statusline.ProgressMidEmpty)
	testIcon("ProgressRightEmpty", statusline.ProgressRightEmpty)
	testIcon("ProgressLeftFull", statusline.ProgressLeftFull)
	testIcon("ProgressMidFull", statusline.ProgressMidFull)
	testIcon("ProgressRightFull", statusline.ProgressRightFull)
	
	// Planet symbols for devspace
	fmt.Println("\nPlanet symbols:")
	testIcon("mercury", "☿")
	testIcon("venus", "♀")
	testIcon("earth", "♁")
	testIcon("mars", "♂")
	testIcon("jupiter", "♃")
	testIcon("default", "●")
	
	// Test combinations as they appear in statusline
	fmt.Println("\nCombinations as they appear in statusline:")
	testCombination("Git with branch", statusline.GitIcon + "main")
	testCombination("AWS with profile", statusline.AwsIcon + "ck-kubero-admin")
	testCombination("K8s with context", statusline.K8sIcon + " kubero-kubero")
	testCombination("Hostname with icon", statusline.HostnameIcon + "vermissian")
	testCombination("Mars devspace", "♂ mars")
}

func testIcon(name, icon string) {
	if icon == "" {
		fmt.Printf("%-20s: [EMPTY] Bytes: 0, Runes: 0, Width: 0\n", name)
		return
	}
	
	bytes := len([]byte(icon))
	runes := len([]rune(icon))
	width := runewidth.StringWidth(icon)
	
	// Visual representation with ruler
	fmt.Printf("%-20s: [%s] Bytes: %d, Runes: %d, Width: %d\n", 
		name, icon, bytes, runes, width)
}

func testCombination(name, text string) {
	width := runewidth.StringWidth(text)
	fmt.Printf("%-20s: [%s] Width: %d\n", name, text, width)
}