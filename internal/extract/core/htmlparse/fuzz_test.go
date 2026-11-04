package htmlparse

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The scan of any bytes gives tokens in order, each inside the input and
// never before the one before, and stops at the end with no error but the
// token limit; a comment's text lies inside its token. Unescape's pieces
// copy the input at their offsets, or stand for a reference that starts
// with & where they say, and decode to what html.UnescapeString gives. Cut
// into blocks at points cuts picks, one scanner over the blocks gives what
// a scanner for each block gives (TestBlocksReadAsAlone). The pres and
// headings the scan finds are the HTML standard's tree builder's.
func FuzzScan(f *testing.F) {
	for _, s := range []string{
		"<!doctype html><p class=x>a &amp; b<script>x<!--y</script><template><p>t</template>",
		"<pre>\n p</pre><textarea>\nq</textarea><!-- c --!><?pi?></ y></><a b='>' c=\"d\">",
		"<!-->", "<!-- a", "<plaintext><b>x", "&nGt;&amp&#x110000;&", "<a\x00b>\r\n\x00", "<tr><td>1<td>2<table>",
	} {
		f.Add(s, uint32(0x00070301))
	}
	f.Fuzz(func(t *testing.T, data string, cuts uint32) {
		src := []byte(data)
		s := NewScanner(src, MaxTokens(int64(len(src))))
		end := 0
		for s.Next() {
			if s.Start() < end || s.End() > len(src) || s.Start() > s.End() {
				t.Fatalf("token %d-%d after %d in %d bytes", s.Start(), s.End(), end, len(src))
			}
			end = s.End()
			if s.Kind() == Comment {
				if from, to, ok := s.CommentText(); ok && (from < s.Start() || to > s.End() || from > to) {
					t.Fatalf("comment text %d-%d outside %d-%d", from, to, s.Start(), s.End())
				}
			}
		}
		if err := s.Err(); err != nil {
			var many *TooManyTokensError
			var nest *NestError
			if !errors.As(err, &many) && !errors.As(err, &nest) {
				t.Fatal(err)
			}
		}
		var out []byte
		Unescape(src, 0, func(b []byte, at int, wrote bool) {
			if !wrote && !bytes.Equal(b, src[at:at+len(b)]) || wrote && src[at] != '&' {
				t.Fatalf("piece %q at %d", b, at)
			}
			out = append(out, b...)
		})
		if want := html.UnescapeString(data); string(out) != want {
			t.Fatalf("decoded %q, want %q", out, want)
		}
		var ends []int
		for i := range 3 {
			if len(data) == 0 {
				break
			}
			if at := int(cuts>>(8*i)&0xff) % len(data); at > 0 && (len(ends) == 0 || at > ends[len(ends)-1]) {
				ends = append(ends, at)
			}
		}
		ends = append(ends, len(data))
		for _, headings := range []bool{false, true} {
			if got, want := strings.Join(joined(data, ends, headings), "\n"), strings.Join(alone(data, ends, headings), "\n"); got != want {
				t.Fatalf("blocks %v, headings %t:\njoined\n%s\nalone\n%s", ends, headings, got, want)
			}
		}
		if extracttest.BrowserComparable(data) {
			sameScanOf(t, data) // the pres and headings are the tree builder's (TestTheScanIsTheBrowsers)
		}
	})
}
