package main

import (
	"os"

	rb "github.com/varnish/route-builder"
)

func main() {
	os.Exit(rb.Run(os.Args[1:], os.Stdout, os.Stderr))
}
