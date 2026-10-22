package properties

import (
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The properties reader is compared with java.util.Properties.load through a
// corpus of generated files and what Java made of each
// (extracttest.OracleCase). Java is not in the CI image, so the corpus is
// committed, and TestRegenerateOracleCorpora writes it again where it is
// installed:
//
//	ERRATA_JAVA=/path/to/jdk/bin/java go test ./internal/extract/properties -run TestRegenerateOracleCorpora
//
// The corpus of this commit came from Temurin 21.0.12.1.
const javaCorpus = "testdata/oracle/properties-java.json"

// TestRegenerateOracleCorpora writes the corpus again from ERRATA_JAVA.
func TestRegenerateOracleCorpora(t *testing.T) {
	java := os.Getenv("ERRATA_JAVA")
	if java == "" {
		t.Skip("set ERRATA_JAVA to write the oracle corpus again")
	}
	extracttest.WriteCorpus(t, javaCorpus, propertiesInputs(), java, "testdata/oracle/PropertiesOracle.java")
}

// propertiesPieces are what generated .properties files are made of: every
// separator, white space, line break, comment character and escape, and the
// escapes Java refuses.
var propertiesPieces = []string{
	"a", "B", "k.x", "=", ":", " ", "\t", "\f", "\\", "\\\\", "\\\n", "\\\r\n", "\\\r", "\n", "\n", "\r\n", "\r",
	"#", "!", "\\u0041", "\\u00e9", "\\uD83D\\uDE00", "\\uD83D", "\\uDE00", "\\u12", "é", "\\t", "\\n",
	"\\z", "\\ ", "\\=", "\\:", "\\#", " \\\n   ", "\n  # c\n", "\n! c\n",
	// White space and line breaks Java does not take, and bytes that are not
	// UTF-8, which Java reads as U+FFFD.
	"\v", "\u0085", "\u2028", "\xff", "\xe2\x82", "\xed\xa0\x80", "\xf0\x9f\x98",
}

func propertiesInputs() []string {
	return extracttest.GenerateCorpus(41, func(r *rand.Rand) string {
		var b strings.Builder
		for range r.IntN(30) {
			b.WriteString(propertiesPieces[r.IntN(len(propertiesPieces))])
		}
		return b.String()
	})
}

// The properties reader reads every file of the corpus as Properties.load
// read it: the same keys and values in order, and a refusal where Java threw.
func TestPropertiesAgreesWithJava(t *testing.T) {
	cases := extracttest.ReadCorpus(t, javaCorpus, propertiesInputs())
	entries, refused := extracttest.CompareOracle(t, cases, readProperties, func(e *core.Entry) []string {
		return []string{string(e.Key), string(e.Value)}
	})
	// The corpus must hold both outcomes in number, or it compares little.
	if entries < extracttest.OracleCases || refused < extracttest.OracleCases/20 {
		t.Errorf("the corpus holds %d entries and %d refusals", entries, refused)
	}
	if testing.Verbose() {
		t.Logf("%d entries, %d refusals", entries, refused)
	}
}
