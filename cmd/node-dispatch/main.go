package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/shitamachi/forgelet/internal/noderuntime"
)

func main() {
	// act caches this path and uses it for all later Actions. Keep the launcher,
	// rather than accidentally pinning every Action to the default Node binary.
	if len(os.Args) == 4 && os.Args[1] == "--no-warnings" && os.Args[2] == "-e" && os.Args[3] == "console.log(process.execPath)" {
		fmt.Println("/usr/local/bin/node")
		return
	}
	version, err := noderuntime.Select(os.Args[1:], os.Getenv("GITHUB_WORKSPACE"))
	if err == nil {
		err = syscall.Exec("/opt/forgelet/"+version+"/bin/node", append([]string{"node"}, os.Args[1:]...), os.Environ())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
