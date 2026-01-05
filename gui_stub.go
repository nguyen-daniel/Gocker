//go:build nogui
// +build nogui

package main

import (
	"fmt"
	"os"
)

// launchGUI is a stub function when GUI is not built
func launchGUI() {
	fmt.Fprintf(os.Stderr, "Error: GUI support not compiled. Build with GUI support using:\n")
	fmt.Fprintf(os.Stderr, "  go build -o gocker main.go gui.go\n")
	fmt.Fprintf(os.Stderr, "Or install X11 development libraries and build normally.\n")
	os.Exit(1)
}

