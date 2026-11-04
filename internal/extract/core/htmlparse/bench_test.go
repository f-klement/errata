package htmlparse

import (
	"strings"
	"testing"
)

// BenchmarkReopen scans paragraphs with nothing to reopen and the same
// after a div closed 36 formatting elements, which a browser reopens
// before each paragraph's text: the Limits row's figure for reconstruction.
func BenchmarkReopen(b *testing.B) {
	var closed strings.Builder
	closed.WriteString("<div>")
	for _, name := range strings.Fields("b big code em font i s small strike strong tt u") {
		for range 3 {
			closed.WriteString("<" + name + ">")
		}
	}
	closed.WriteString("</div>")
	for _, c := range []struct{ name, prefix string }{{"none", ""}, {"36", closed.String()}} {
		src := []byte(c.prefix + strings.Repeat("<p class=aaaa>xxxxxxx</p>", 8<<20/25))
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				s := NewScanner(src, MaxTokens(int64(len(src))))
				s.Headings()
				for s.Next() {
				}
				if s.Err() != nil {
					b.Fatal(s.Err())
				}
			}
		})
	}
}
