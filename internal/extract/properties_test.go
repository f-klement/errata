package extract

import (
	"testing"
)

func props(key string) Spec {
	return Spec{Properties: &Properties{File: "a.properties", Key: key}}
}

// The examples of the java.util.Properties.load documentation, each with
// the key and the value it gives there.
func TestPropertiesReadsTheJavadocExamples(t *testing.T) {
	for _, c := range []struct{ body, key, want string }{
		{"Truth = Beauty\n", "Truth", "Beauty@1"},
		{"  Truth:Beauty\n", "Truth", "Beauty@1"},
		{"Truth                    :Beauty\n", "Truth", "Beauty@1"},
		{"fruits                           apple, banana, pear, \\\n" +
			"                                  cantaloupe, watermelon, \\\n" +
			"                                  kiwi, mango\n", "fruits", "apple, banana, pear, cantaloupe, watermelon, kiwi, mango@1"},
		{"cheeses\n", "cheeses", "@1"},
		{"\\:\\= = x\n", ":=", "x@1"},
	} {
		if got := iniValues(t, "a.properties", c.body, props(c.key)); got != c.want {
			t.Errorf("%q, key %q: got %q, want %q", c.body, c.key, got, c.want)
		}
	}
}

// Each rule of LineReader and loadConvert. Every case was run through
// Properties.load of Temurin 21.0.12.1, which gives the same values but for
// the two with a surrogate alone, which Java keeps and UTF-8 cannot hold.
func TestPropertiesLines(t *testing.T) {
	for _, c := range []struct{ name, body, key, want string }{
		{"a dotted key", "server.port=8080\nserver.port.x=1\n", "server.port", "8080@1"},
		{"a key set twice", "a=1\nb=2\na=3\n", "a", "1@1 3@3"},
		{"comments", "# a=1\n  ! a=2\na=3 # kept\n", "a", "3 # kept@3"},
		{"a comment is never joined", "# x \\\na=1\n", "a", "1@2"},
		{"a joined line may start with #", "a=1 \\\n  # 2\n", "a", "1 # 2@1"},
		{"a lone \\ before a comment", "\\\n#a=1\na=2\n", "a", "2@3"},
		{"the comment after a lone \\ sets no key", "\\\n#a=1\na=2\n", "#a", ""},
		{"a lone \\ before a key", "\\\n  a=1\n", "a", "1@2"},
		{"an even run of \\ ends the line", "a=x\\\\\nb=y\n", "a", "x\\@1"},
		{"an odd run of \\ joins", "a=x\\\\\\\nb=y\n", "a", "x\\b=y@1"},
		{"\\ at the end of the file", "a=x\\", "a", "x@1"},
		{"\\ and a newline at the end", "a=x\\\n", "a", "x@1"},
		{"a joined empty line ends it", "a=x\\\n\nb=y\n", "a", "x@1"},
		{"a joined blank line ends it", "a=x\\\n   \nb=y\n", "b", "y@3"},
		{"CR ends a line", "a=1\rb=2\r", "b", "2@1"},
		{"CRLF", "a=1\r\nb=2\r\n", "b", "2@2"},
		{"\\ and CRLF joins", "a=1\\\r\n  2\r\nb=3\r\n", "a", "12@1"},
		{"\\ and CR joins", "a=1\\\r  2\rb=3\r", "a", "12@1"},
		{"form feed is white space", "\fa\f=\f1\n", "a", "1@1"},
		{"one separator, then white space", "a = = 1\n", "a", "= 1@1"},
		{"white space, then a separator", "a   :  1\n", "a", "1@1"},
		{"white space alone separates", "a b c\n", "a", "b c@1"},
		{"an escaped space in a key", "a\\ b = c\n", "a b", "c@1"},
		{"escapes", "a=\\t\\n\\r\\f\\b\\z\\\\\\\"\\'\n", "a", "\t\n\r\fbz\\\"'@1"},
		{"a unicode escape", "a=\\u0041\\u00e9\\u20AC\n", "a", "Aé€@1"},
		{"an escaped key", "\\u0061=1\n", "a", "1@1"},
		{"a surrogate pair", "a=\\uD83D\\uDE00\n", "a", "\U0001F600@1"},
		{"a surrogate alone", "a=\\uD83Dx\\uDE00\n", "a", "\uFFFDx\uFFFD@1"},
		{"two high surrogates", "a=\\uD83D\\uD83D\\uDE00\n", "a", "\uFFFD\U0001F600@1"},
		{"text that is not ASCII", "é=ü\n", "é", "ü@1"},
		{"blank lines first", "\n\n  \t\n\na=1\n", "a", "1@5"},
		{"the line of the key", "x=1\n\n  key \\\n = value\n", "key", "value@3"},
		{"no line break at the end", "a=1", "a", "1@1"},
		{"a byte order mark is a character", "\xef\xbb\xbfa=1\n", "a", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniValues(t, "a.properties", c.body, props(c.key)); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The planted breaks: a \u with fewer than four hex digits, in a key and in
// a value, which Properties.load refuses with IllegalArgumentException.
func TestPropertiesRefusesAMalformedUnicodeEscape(t *testing.T) {
	for body, want := range map[string]string{
		"a=\\u12\n":        "a.properties: line 1: the value: a \\u is not followed by four hex digits, which Properties.load refuses",
		"x=1\na=\\u12g4\n": "a.properties: line 2: the value: a \\u is not followed by four hex digits, which Properties.load refuses",
		"\\uXYZW=1\n":      "a.properties: line 1: the key: a \\u is not followed by four hex digits, which Properties.load refuses",
		"a=\\uD83D\\u12":   "a.properties: line 1: the value: a \\u is not followed by four hex digits, which Properties.load refuses",
	} {
		if got := iniError(t, "a.properties", body, props("a")); got != want {
			t.Errorf("%q: error %q, want %q", body, got, want)
		}
	}
}
