// Command compose-oracle reads each file it is given as docker compose reads
// a .env file, with compose-go's dotenv parser and an empty environment, and
// prints one line of JSON for each: the keys and values compose set, sorted
// by key and each as the hex of its bytes, since JSON holds no byte that is
// not UTF-8, or the error it refused the file with.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/sirupsen/logrus"
)

type result struct {
	Entries [][]string `json:"entries,omitempty"`
	Refused string     `json:"refused,omitempty"`
}

func main() {
	logrus.SetOutput(io.Discard)
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for _, f := range os.Args[1:] {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var r result
		vars, err := dotenv.ParseWithLookup(bytes.NewReader(data), nil)
		if err != nil {
			r.Refused = err.Error()
			if r.Refused == "" {
				r.Refused = "refused"
			}
		} else {
			keys := make([]string, 0, len(vars))
			for k := range vars {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				r.Entries = append(r.Entries, []string{hex.EncodeToString([]byte(k)), hex.EncodeToString([]byte(vars[k]))})
			}
		}
		if err := enc.Encode(r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
