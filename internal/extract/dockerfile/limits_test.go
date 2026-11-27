package dockerfile

import (
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

func entryExtractor(t *testing.T, s Spec) core.EntryExtractor {
	t.Helper()
	if s.File == "" {
		s.File = "Dockerfile"
	}
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	return e.(core.EntryExtractor)
}

// ordinary is a Dockerfile as projects write one: a builder stage and a
// final one, global ARGs, and LABEL and ENV over continued lines.
const ordinary = `# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.23
ARG ALPINE_VERSION=3.20

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
ENV CGO_ENABLED=0 \
    GOFLAGS="-trimpath -mod=readonly"
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/app ./cmd/app

FROM alpine:${ALPINE_VERSION}
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://example.com/app" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"
ENV PORT=8080 \
    LOG_LEVEL=info
EXPOSE ${PORT}/tcp 9090
COPY --from=build /out/app /usr/local/bin/app
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/app"]
`

// The values of the ordinary file, each where it is written. Planted
// break: the global ARGs not reaching the FROM.
func TestOrdinaryDockerfile(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Instruction: "FROM"}, "golang:1.23-alpine3.20@5 alpine:3.20@15"},
		{Spec{Instruction: "LABEL", Key: "org.opencontainers.image.version"}, "org.opencontainers.image.version=dev@18"},
		{Spec{Instruction: "ENV", Stage: "build"}, "CGO_ENABLED=0@8 GOFLAGS=-trimpath -mod=readonly@9"},
		{Spec{Instruction: "EXPOSE"}, "8080/tcp@22 9090@22"},
		{Spec{Instruction: "ARG", Stage: "1"}, "VERSION=dev@16"},
		{Spec{Instruction: "ARG", Key: "TARGETARCH"}, "!TARGETARCH=@6"},
	} {
		if got := mustRead(t, c.spec, ordinary); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.spec, got, c.want)
		}
	}
}

// The exact work of a small read, step by step, so a charge of the wrong
// size fails: each byte of the file, each instruction's LineWork, each
// byte of every word expanded (a, then each key and value), the matcher's
// look at each ENV entry, 48 and the 1 byte of the query's key, and the
// lookup of A, whose 1 byte of value is charged 32 and the 2 bytes of the
// word $A. The separator = costs no search. Planted breaks: each charge
// dropped or doubled, the separator searched for.
func TestWorkIsExact(t *testing.T) {
	x := entryExtractor(t, Spec{Instruction: "ENV", Key: "B"})
	data := "FROM a\nENV B=$A\n"
	got := coretest.Spent(t, x, []byte(data))
	want := int64(len(data)) + 2*core.LineWork + 1 + 1 + 2 + 48 + 1
	if got != want {
		t.Errorf("spent %d, want %d", got, want)
	}
	data = "FROM a\nENV A=1\nENV B=$A\n"
	got = coretest.Spent(t, x, []byte(data))
	want = int64(len(data)) + 3*core.LineWork + 1 + (1 + 1 + 48 + 1) + (1 + 2 + (32+2)*1 + 48 + 1)
	if got != want {
		t.Errorf("spent %d, want %d", got, want)
	}
}

// Each read has a budget of work, which every byte, instruction, word and
// lookup is charged: below its cost a read is refused, at it the read
// returns what it reads with no meter.
func TestWorkMeterRefusesAtEveryBudget(t *testing.T) {
	coretest.RefusesAtEveryBudget(t, entryExtractor(t, Spec{Instruction: "ENV"}), "ARG A=1\nFROM a\nARG A\nENV B=${A}x \\\n  C=\"$B\" \\\n  D=1\n")
}

// Ordinary Dockerfiles spend under a quarter of the budget a byte.
func TestWorkMeterLeavesRoomForOrdinaryFiles(t *testing.T) {
	coretest.LeavesRoom(t, entryExtractor(t, Spec{Instruction: "LABEL", Key: "org.opencontainers.image.version"}), ordinary)
	coretest.LeavesRoom(t, entryExtractor(t, Spec{Instruction: "ENV"}), ordinary)
}

// A chain of variables that each double the one before is refused for
// work before its values fill memory, as envfile's is. Planted break: the
// lookup's charge left out, when the chain's last value would be 2^60
// bytes.
func TestRefusesADoublingChain(t *testing.T) {
	var b strings.Builder
	b.WriteString("FROM a\nENV A0=xxxxxxxxxxxxxxxx\n")
	for i := 1; i <= 60; i++ {
		b.WriteString("ENV A" + strconv.Itoa(i) + "=$A" + strconv.Itoa(i-1) + "$A" + strconv.Itoa(i-1) + "\n")
	}
	_, err := read(t, Spec{Instruction: "ENV"}, b.String())
	extracttest.WantError(t, err, "Dockerfile: reading the file and matching the spec take more than 128 units of work")
}

// The extractor returns at most its value limit and refuses past it, and
// its values hold at most max_bytes in all, as every extractor's.
func TestValueLimits(t *testing.T) {
	x := entryExtractor(t, Spec{Instruction: "EXPOSE"})
	x.ValueLimit = 3
	if got, err := x.FromData([]byte("FROM a\nEXPOSE 1 2 3\n")); err != nil || len(got) != 3 {
		t.Fatalf("%d values, %v", len(got), err)
	}
	_, err := x.FromData([]byte("FROM a\nEXPOSE 1 2 3 4\n"))
	extracttest.WantError(t, err, "Dockerfile: the extractor selects more than 100000 values, the limit")
	// The file may be a 32nd of max_bytes (parseShare), so only values
	// that expansion lengthens can pass max_bytes: nine doublings of 8
	// bytes in 121.
	x = entryExtractor(t, Spec{OneFile: core.OneFile{File: "Dockerfile", MaxBytes: 4096}, Instruction: "ENV"})
	var data strings.Builder
	data.WriteString("FROM a\nENV A=aaaaaaaa\n")
	for c := 'B'; c <= 'J'; c++ {
		data.WriteString("ENV " + string(c) + "=$" + string(c-1) + "$" + string(c-1) + "\n")
	}
	_, err = x.FromData([]byte(data.String()))
	extracttest.WantError(t, err, "Dockerfile: the values the extractor selects hold more than 4096 bytes in all")
}

// A Dockerfile over max_bytes divided by parseShare is refused before
// BuildKit's parser sees it, so the refusal allocates less than the file,
// where the parse of the densest shape, ENV pairs of no value, allocates
// some 470 times the file; a file at the limit reads. The default limit
// is 1 MiB, for Build's extractor and Every's. Planted breaks: the check
// left out, the limit taken as max_bytes itself in Build and in Every.
func TestRefusesAFileOverTheParseLimit(t *testing.T) {
	const maxBytes, limit = 1 << 20, 1 << 15
	x := entryExtractor(t, Spec{OneFile: core.OneFile{File: "Dockerfile", MaxBytes: maxBytes}, Instruction: "ENV"})
	head := "FROM a\nENV A="
	at := head + strings.Repeat("x", limit-len(head)-1) + "\n"
	if got, err := x.FromData([]byte(at)); err != nil || len(got) != 1 {
		t.Fatalf("a file at the limit: %d values, %v", len(got), err)
	}
	_, err := x.FromData([]byte(at + "\n"))
	extracttest.WantError(t, err, "Dockerfile: the file is 32769 bytes, over the limit of 32768")
	dense := []byte("FROM a\nENV " + strings.Repeat("a= ", limit/3) + "\n")
	if n := extracttest.Allocated(func() { _, err = x.FromData(dense) }); n > uint64(len(dense)) {
		t.Errorf("the refusal allocated %d bytes, more than the file's %d", n, len(dense))
	}
	want := "Dockerfile: the file is " + strconv.Itoa(len(dense)) + " bytes, over the limit of 32768 that BuildKit's parser is given, max_bytes divided by 32; set max_bytes: higher to read a larger Dockerfile"
	extracttest.WantError(t, err, want)
	over := []byte("FROM a\n" + strings.Repeat("ENV A=1\n", 1<<17))
	_, err = entryExtractor(t, Spec{Instruction: "ENV"}).FromData(over)
	extracttest.WantError(t, err, "over the limit of 1048576 that BuildKit's parser is given")
	every, err := Every("Dockerfile", "A", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = every.(core.EntryExtractor).FromData(over)
	extracttest.WantError(t, err, "over the limit of 1048576 that BuildKit's parser is given")
}

// A panic in BuildKit's parser or lexer refuses the file with what it
// said, and errata goes on to the next rule (ADR 0020). Planted breaks:
// the recover left out, the lexer called unguarded.
func TestParserPanicRefusesTheFile(t *testing.T) {
	defer func(p func(io.Reader) (*parser.Result, error)) { parse = p }(parse)
	parse = func(io.Reader) (*parser.Result, error) { panic("index out of range") }
	_, err := read(t, Spec{Instruction: "ENV"}, "FROM a\n")
	extracttest.WantError(t, err, "Dockerfile: BuildKit's Dockerfile parser stopped on the file: index out of range")
}

func TestLexerPanicRefusesTheFile(t *testing.T) {
	defer func(p func(*shell.Lex, string, shell.EnvGetter) (shell.ProcessWordResult, error)) { processWord = p }(processWord)
	processWord = func(*shell.Lex, string, shell.EnvGetter) (shell.ProcessWordResult, error) { panic("nil map") }
	_, err := read(t, Spec{Instruction: "ENV"}, "FROM a\nENV A=1\n")
	extracttest.WantError(t, err, "Dockerfile: BuildKit's Dockerfile lexer stopped on the file: nil map")
}

// A panic in errata's own reading is not recovered, so the tests and the
// fuzz target see it rather than a refusal. Planted break: the recover
// around all of read, as it was.
func TestOwnPanicIsNotRecovered(t *testing.T) {
	defer func() {
		if v := recover(); v != "errata's own" {
			t.Errorf("recovered %v, want errata's own panic", v)
		}
	}()
	sel := selection{instruction: "env", parseLimit: 1 << 20}
	err := sel.read([]byte("FROM a\nENV A=1\n"), core.NewMeter(16), func(*core.Entry) error { panic("errata's own") })
	t.Errorf("read returned %v, want a panic", err)
}
