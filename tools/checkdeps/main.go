// Command checkdeps fails when core or the CLI depends on MyGo: they must
// build and run without it (docs/DESIGN.md, 6). The one exception is
// github.com/egoist/mygo/netproxy, a package of its own.
//
//	go run ./tools/checkdeps
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	roots   = []string{"./core/...", "./cmd/postgo-cli/..."}
	allowed = map[string]bool{"github.com/egoist/mygo/netproxy": true}
)

func main() {
	for _, root := range roots {
		// -e keeps going when a root has no packages yet.
		out, err := exec.Command("go", "list", "-e", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", root).Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "checkdeps:", root, err)
			os.Exit(1)
		}
		var bad []string
		for _, p := range strings.Fields(string(out)) {
			if strings.HasPrefix(p, "github.com/egoist/mygo") && !allowed[p] {
				bad = append(bad, p)
			}
		}
		if len(bad) > 0 {
			fmt.Fprintf(os.Stderr, "checkdeps: %s depends on MyGo:\n  %s\n", root, strings.Join(bad, "\n  "))
			os.Exit(1)
		}
	}
	fmt.Println("checkdeps: ok")
}
