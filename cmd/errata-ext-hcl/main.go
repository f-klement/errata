// Command errata-ext-hcl is errata's HCL extractor plugin: errata runs it
// once for each request of plugin protocol 1, with the request on stdin,
// and reads the reply from stdout (docs/plugin-protocol.md). The kind it
// provides is internal/ext/hcl's.
package main

import (
	"os"

	"github.com/f-klement/errata/internal/ext/hcl"
)

func main() { os.Exit(hcl.Main(os.Stdin, os.Stdout, os.Stderr)) }
