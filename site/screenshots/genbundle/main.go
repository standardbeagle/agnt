// Command genbundle writes the exact proxy-injected __devtool script bundle to
// stdout so the screenshot harness can embed it into a static test page. Run:
//
//	go run ./site/screenshots/genbundle > site/screenshots/bundle.html
package main

import (
	"fmt"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

func main() {
	fmt.Print(scripts.GetCombinedScript())
}
