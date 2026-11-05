// Command stock reads the pantry database through the larder API and prints
// what runs low.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

// Port is a port of the larder API on the host the checker runs on.
type Port int

const (
	// APIPort serves the stock the checker reads.
	APIPort Port = 8080 + iota
	// MetricsPort serves the API's metrics, one above it.
	MetricsPort
)

func main() {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/stock?low=true", APIPort))
	if err != nil {
		fmt.Fprintln(os.Stderr, "stock:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		fmt.Fprintln(os.Stderr, "stock:", err)
		os.Exit(1)
	}
}
