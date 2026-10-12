// Command metamusic is the single-binary entrypoint (stub until A3).
package main

import (
	"fmt"
	"os"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("metamusic %s (%s)\n", version, commit)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: metamusic version")
	os.Exit(2)
}
