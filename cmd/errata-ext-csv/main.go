// Command errata-ext-csv is errata's CSV extractor plugin: errata runs it
// once for each request of plugin protocol 1, with the request on stdin,
// and reads the reply from stdout (docs/plugin-protocol.md). The kind it
// provides is internal/ext/csv's.
package main

import (
	"os"

	"github.com/f-klement/errata/internal/ext/csv"
)

func main() { os.Exit(csv.Main(os.Stdin, os.Stdout, os.Stderr)) }
