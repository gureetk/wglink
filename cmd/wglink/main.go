// Wglink connects Linux machines over WireGuard.
package main

import (
	"os"

	"codeberg.org/gureetk/wglink/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1) // cobra has already printed the error
	}
}
