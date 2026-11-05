package lineend

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// breaks finds the line ends as a reader of the HTML standard writes them,
// CRLF before CR, so the tests compare against a rule written another way.
var breaks = regexp.MustCompile(`\r\n|\r|\n`)

// each calls f with every text of up to six bytes of a, CR and LF: every
// way two line ends can stand next to each other, at either end of a text
// or a range.
func each(f func(s string)) {
	texts := []string{""}
	for range 6 {
		var next []string
		for _, t := range texts {
			for _, c := range []string{"a", "\r", "\n"} {
				next = append(next, t+c)
			}
		}
		for _, t := range texts {
			f(t)
		}
		texts = next
	}
	for _, t := range texts {
		f(t)
	}
}

// Line, Start and Next agree with the line ends the regex finds at every
// offset: a byte's line is one more than the line ends before it, the LF of
// a CRLF on the CR's line. The planted breaks: a CR counted before a LF
// (Count), the LF of a CRLF given a line of its own (Start), and a CRLF
// ended after its CR (Next).
func TestTheLinesAreTheRegexs(t *testing.T) {
	each(func(s string) {
		data := []byte(s)
		ends := breaks.FindAllStringIndex(s, -1)
		for off := 0; off <= len(s); off++ {
			line, start, next := 1, 0, len(s)
			for _, e := range ends {
				if e[1] <= off {
					line, start = line+1, e[1]
				} else if next == len(s) {
					next = e[1]
				}
			}
			if got := Line(data, off); got != line {
				t.Fatalf("Line(%q, %d) = %d, want %d", s, off, got, line)
			}
			if got := Start(data, off); got != start {
				t.Fatalf("Start(%q, %d) = %d, want %d", s, off, got, start)
			}
			if off < len(s) {
				if got := Next(data, off); got != next {
					t.Fatalf("Next(%q, %d) = %d, want %d", s, off, got, next)
				}
			}
		}
	})
}

// Starts is 0 and the end of each line end the regex finds. The planted
// break: a CR before a LF taken as a line end of its own.
func TestStartsAreAfterEachLineEnd(t *testing.T) {
	each(func(s string) {
		want := []int{0}
		for _, e := range breaks.FindAllStringIndex(s, -1) {
			want = append(want, e[1])
		}
		if got := Starts([]byte(s)); !slices.Equal(got, want) {
			t.Fatalf("Starts(%q) = %v, want %v", s, got, want)
		}
	})
}

// Count over a range is the line ends the regex finds ending inside it, a
// CRLF counted where its LF is, so the ranges of a split add up to the
// whole. The planted break: a lone CR at the end of the text not counted.
func TestCountAddsUpOverAnySplit(t *testing.T) {
	each(func(s string) {
		data := []byte(s)
		whole := len(breaks.FindAllStringIndex(s, -1))
		if got := Count(data, 0, len(s)); got != whole {
			t.Fatalf("Count(%q) = %d, want %d", s, got, whole)
		}
		for mid := 0; mid <= len(s); mid++ {
			if a, b := Count(data, 0, mid), Count(data, mid, len(s)); a+b != whole {
				t.Fatalf("%q split at %d: %d + %d, want %d", s, mid, a, b, whole)
			}
		}
	})
}

// Fold reads each line end as one LF and keeps every other byte, and hands
// back a text with no CR as it is. The planted breaks: a CRLF folded to two
// LFs, and a lone CR kept.
func TestFoldIsOneLFForEachLineEnd(t *testing.T) {
	each(func(s string) {
		if got, want := string(Fold([]byte(s))), breaks.ReplaceAllString(s, "\n"); got != want {
			t.Fatalf("Fold(%q) = %q, want %q", s, got, want)
		}
	})
	data := []byte("a\nb")
	if &Fold(data)[0] != &data[0] {
		t.Error("Fold copied a text with no CR")
	}
	if got := string(Fold([]byte("a\r\rb"))); got != "a\n\nb" {
		t.Errorf("a\\r\\rb: got %q", got)
	}
	if strings.Contains(string(Fold([]byte("\r\r\n\r"))), "\r") {
		t.Error("a CR is left")
	}
}
