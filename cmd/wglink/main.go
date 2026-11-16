// Wglink connects Linux machines over WireGuard.
package main

import (
	"os"

	"github.com/gureetk/wglink/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
