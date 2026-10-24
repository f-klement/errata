package envfile

import (
	"encoding/hex"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The envfile reader is compared with compose-go's dotenv parser, which
// docker compose reads .env and env_file files with, through a corpus of
// generated files and what compose made of each with an empty environment
// (extracttest.OracleCase), each key and value as the hex of its bytes.
// compose-go is not a dependency of errata, so
// the oracle is a program of its own module in testdata/oracle/compose,
// and the corpus is committed. TestRegenerateOracleCorpora writes it again:
//
//	(cd testdata/oracle/compose && go build -o /tmp/compose-oracle .)
//	ERRATA_COMPOSE_ORACLE=/tmp/compose-oracle go test ./internal/extract/envfile -run TestRegenerateOracleCorpora
//
// The corpus of this commit came from compose-go v2.16.2.
const composeCorpus = "testdata/oracle/envfile-compose.json"

// TestRegenerateOracleCorpora writes the corpus again from
// ERRATA_COMPOSE_ORACLE.
func TestRegenerateOracleCorpora(t *testing.T) {
	oracle := os.Getenv("ERRATA_COMPOSE_ORACLE")
	if oracle == "" {
		t.Skip("set ERRATA_COMPOSE_ORACLE to write the oracle corpus again")
	}
	extracttest.WriteCorpus(t, composeCorpus, envInputs(), oracle)
}

// envPieces are what generated env files are made of: every separator,
// quote, escape, comment and substitution form, and the white space, line
// breaks and bytes compose reads apart.
var envPieces = []string{
	"A", "b", "K_1", "x.y", "a-b[0]", "1", "=", ":", " ", "\t", "\n", "\n", "\r\n", "#", " #", "export ", "export",
	"'", "\"", "\\", "\\'", "\\\"", "\\\\", "\\n", "\\t", "\\$", "\\0101", "\\012", "\\0", "\\c", "\\q",
	"$", "$$", "${", "}", "{", "$A", "${A}", "${b:-d}", "${A-x}", "${Z:+y}", "${A+y}", "${A?}", "${A:?m}",
	"${Z}", "$Z", ":-", "${A:-${b}}", "${b:-{}}", "${A:x}", "${1}",
	"é", "\u00a0", "\u0085", "\u2028", "\u3000", "\u212a", "\u017f", "\v", "\f", "\xff", "\xc3", "\ufeff",
}

// envLines are lines of a key, a separator and a value made of pieces, so
// a corpus holds files compose reads besides the many it refuses.
var (
	envKeys   = []string{"A", "b", "Z", "K_1", "x.y", "export A", "\u212a", ""}
	envSeps   = []string{"=", "=", "=", " = ", ":", ": "}
	envValues = []string{"", "1", "x y", "'a b'", "\"a b\"", "\"a\nb\"", "'a\nb'", "x #c", "x#c", "${A}", "$A",
		"\"${A}\"", "'${A}'", "${Z:-d}", "${A:-d}", "${A:+y}", "${Z}x", "\"\\$A\"", "$$A", "\"\\n\\0101\"",
		"${A:?}", "${Z?m}", "${A-${b}}", "\"x\" y", "'it\\'s'"}
)

func envInputs() []string {
	return extracttest.GenerateCorpus(33, func(r *rand.Rand) string {
		var b strings.Builder
		if r.IntN(4) == 0 {
			for range r.IntN(30) {
				b.WriteString(envPieces[r.IntN(len(envPieces))])
			}
			return b.String()
		}
		// Half the files set A first, which the substitutions name.
		if r.IntN(2) == 0 {
			b.WriteString(envKeys[0] + "=" + envValues[r.IntN(4)] + "\n")
		}
		for range 2 + r.IntN(8) {
			b.WriteString(envKeys[r.IntN(len(envKeys))] + envSeps[r.IntN(len(envSeps))])
			b.WriteString(envValues[r.IntN(len(envValues))])
			if r.IntN(8) == 0 {
				b.WriteString(envPieces[r.IntN(len(envPieces))])
			}
			b.WriteString("\n")
		}
		return b.String()
	})
}

// The envfile reader reads every file of the corpus as compose read it:
// the last literal value of each key is compose's value, compose sets no
// key the reader does not, and the reader refuses where compose refused.
// A value the reader marks computed depends on the environment, so it is
// not compared: compose read it with an empty one. Where compose refused
// a substitution that only an empty environment refuses, such as ${Z?} of
// a name the file does not set, or ${Z:-${} whose word compose reads only
// when Z is unset, the reader reads the file and marks a value computed.
// A refusal of the file's syntax is never one of these.
func TestEnvfileAgreesWithCompose(t *testing.T) {
	cases := extracttest.ReadCorpus(t, composeCorpus, envInputs())
	compared, computedValues, refused := 0, 0, 0
	for i, c := range cases {
		type last struct {
			value    string
			computed bool
		}
		got := map[string]last{}
		anyComputed := false
		err := readEnv(c.File(), func(e *core.Entry) error {
			got[string(e.Key)] = last{string(e.Value), e.Computed}
			anyComputed = anyComputed || e.Computed
			return nil
		})
		switch {
		case c.Refused != "" && err == nil:
			if !anyComputed || !strings.Contains(c.Refused, "required variable") && !strings.Contains(c.Refused, "Invalid template") {
				t.Errorf("case %d %q: compose refuses it (%s) and errata reads %v", i, c.Input, c.Refused, got)
			}
			computedValues++
			continue
		case c.Refused == "" && err != nil:
			t.Errorf("case %d %q: compose reads %q and errata refuses it: %v", i, c.Input, c.Entries, err)
			continue
		case c.Refused != "":
			refused++
			continue
		}
		want := map[string]string{}
		for _, kv := range c.Entries {
			k, kerr := hex.DecodeString(kv[0])
			v, verr := hex.DecodeString(kv[1])
			if kerr != nil || verr != nil {
				t.Fatalf("case %d: an entry not in hex: %q", i, kv)
			}
			want[string(k)] = string(v)
			if _, ok := got[string(k)]; !ok {
				t.Errorf("case %d %q: compose sets %q=%q and errata reads no such key", i, c.Input, k, v)
			}
		}
		for k, v := range got {
			if v.computed {
				computedValues++
				continue
			}
			if w, ok := want[k]; !ok || w != v.value {
				t.Errorf("case %d %q: errata reads %q=%q, compose %q (set: %v)", i, c.Input, k, v.value, w, ok)
			}
			compared++
		}
	}
	// The corpus must hold each outcome in number, or it compares little.
	if compared < extracttest.OracleCases || refused < extracttest.OracleCases/20 || computedValues < extracttest.OracleCases/20 {
		t.Errorf("the corpus compares %d values, %d computed and %d refusals", compared, computedValues, refused)
	}
	if testing.Verbose() {
		t.Logf("%d values compared, %d computed, %d refusals", compared, computedValues, refused)
	}
}
