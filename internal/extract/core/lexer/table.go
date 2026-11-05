package lexer

// The forms several rows share.
var (
	slashLine   = Form{Kind: Comment, Open: "//"}
	slashBlock  = Form{Kind: Comment, Open: "/*", Close: "*/"}
	nestedBlock = Form{Kind: Comment, Open: "/*", Close: "*/", Nests: true}
	hashLine    = Form{Kind: Comment, Open: "#"}
	dashLine    = Form{Kind: Comment, Open: "--"}
	markupBlock = Form{Kind: Comment, Open: "<!--", Close: "-->"}

	// A string that ends at its line, with a backslash escape.
	dquoteLine = Form{Kind: String, Open: `"`, Close: `"`, Escape: '\\', Line: true}
	squoteLine = Form{Kind: String, Open: "'", Close: "'", Escape: '\\', Line: true}
	// A string that may run over lines, with a backslash escape.
	dquote = Form{Kind: String, Open: `"`, Close: `"`, Escape: '\\'}
	squote = Form{Kind: String, Open: "'", Close: "'", Escape: '\\'}
	// A string of three quotes over lines, with and without an escape.
	dtriple = Form{Kind: String, Open: `"""`, Close: `"""`, Escape: '\\'}
	striple = Form{Kind: String, Open: "'''", Close: "'''", Escape: '\\'}
	// Kotlin's raw string, whose closer takes the quotes after it: """a"""" is a".
	dtripleGreedy = Form{Kind: String, Open: `"""`, Close: `"""`, Greedy: true}
	stripleRaw    = Form{Kind: String, Open: "'''", Close: "'''"}
	// A character literal: one character or one escape between quotes.
	char = Form{Kind: String, Open: "'", Close: "'", Escape: '\\', Short: true}
	// A JavaScript template literal, a Go raw string.
	backtick    = Form{Kind: String, Open: "`", Close: "`", Escape: '\\'}
	backtickRaw = Form{Kind: String, Open: "`", Close: "`"}
	// An attribute value of a markup tag, after its =.
	attrDquote = Form{Kind: String, Open: `"`, Close: `"`, After: "="}
	attrSquote = Form{Kind: String, Open: "'", Close: "'", After: "="}
)

// cppDelimiter are the bytes a C++ raw string's delimiter may hold: any
// printable byte but a space, the parentheses and the backslash.
const cppDelimiter = "!\"#$%&'*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[]^_`abcdefghijklmnopqrstuvwxyz{|}~"

// maxFence is the longest fence a form of the table reads, past which its
// opener opens nothing. A fence of Swift's #, C#'s quotes, Rust's # or Lua's =
// may be any length in the language, but a fence is matched again at every
// byte the opener or closer could start at, so an unbounded fence makes the
// walk quadratic on a run of its byte. With the bound, a run of Swift's #,
// the slowest shape, costs each # two fences of up to 16 bytes. A fence past
// its bound makes the file's rule unknown (Form.FenceMax).
const maxFence = 16

// maxTag is the longest PostgreSQL dollar quote tag, an identifier of up to
// 63 bytes. Its bytes are neither the opener's nor the closer's first byte, so
// no tag byte is where another fence could start, and a long bound keeps the
// walk linear.
const maxTag = 63

// table is one row per language. Adding a language is one row.
var table = []Language{
	// C-like.
	{Name: "go", Family: "c", Extensions: []string{".go"},
		Forms: []Form{slashLine, slashBlock, dquoteLine, char, backtickRaw}},
	{Name: "c", Family: "c", Extensions: []string{".c", ".h"},
		Forms: []Form{slashLine, slashBlock, dquoteLine, char}},
	{Name: "cpp", Family: "c", Extensions: []string{".cc", ".cpp", ".cxx", ".c++", ".hh", ".hpp", ".hxx", ".h++"},
		Forms: []Form{slashLine, slashBlock, dquoteLine, char,
			{Kind: String, Open: `R"`, Fence: cppDelimiter, FenceMax: 16, Inner: "(", Close: ")", Outer: `"`}}},
	{Name: "java", Family: "c", Extensions: []string{".java"},
		Forms: []Form{slashLine, slashBlock, dtriple, dquoteLine, char}},
	{Name: "kotlin", Family: "c", Extensions: []string{".kt", ".kts"}, Syntax: &kotlin,
		Forms: []Form{slashLine, nestedBlock, interp(dtripleGreedy, "$"), interp(dquoteLine, "$"), char}},
	{Name: "javascript", Family: "c", Extensions: []string{".js", ".mjs", ".cjs", ".jsx"}, Syntax: &javascript,
		Forms: []Form{slashLine, slashBlock, dquoteLine, squoteLine, interp(backtick, "${"), regex}},
	{Name: "typescript", Family: "c", Extensions: []string{".ts", ".mts", ".cts", ".tsx"}, Syntax: &typescript,
		Forms: []Form{slashLine, slashBlock, dquoteLine, squoteLine, interp(backtick, "${"), regex}},
	// A PHP file is text with code between <?php, <?= or <? and ?>: the text
	// is a span of kind Text, and a line comment ends at ?>.
	{Name: "php", Family: "c", Extensions: []string{".php"}, Syntax: &php, Begin: "<?",
		Forms: []Form{{Kind: Comment, Open: "//", Stop: "?>"}, {Kind: Comment, Open: "#", Stop: "?>"}, slashBlock,
			quoteOnly(squote), withCode(interp(dquote, "$"), "${", "{$"),
			{Kind: Text, Open: "?>", Close: "<?", ToEnd: true, Then: []string{"php", "=", " ", "\t", "\n", "\r"}},
			{Kind: String, Open: "`", Close: "`", Escape: '\\', Computed: true},
			withCode(trimLast(heredoc("<<<", "", true, true, DedentCloser, "$")), "${", "{$")}},
	{Name: "groovy", Family: "c", Extensions: []string{".groovy", ".gradle", ".gvy"}, Syntax: &groovy,
		Forms: []Form{slashLine, slashBlock, striple, interp(dtriple, "$"), squoteLine, interp(dquoteLine, "$"),
			{Kind: String, Open: "$/", Close: "/$", Interp: "$"}, interp(regexLines, "$")}},
	{Name: "scala", Family: "c", Extensions: []string{".scala", ".sc", ".sbt"}, Syntax: &scala,
		// The s, f and raw prefixes make a string interpolate: its ${...} is
		// code. A string with another prefix, a custom interpolator, is read
		// as a plain string, whose ${ is text.
		Forms: append([]Form{slashLine, nestedBlock, dtripleGreedy, dquoteLine, char}, interpolators()...)},
	{Name: "rust", Family: "c", Extensions: []string{".rs"},
		Forms: []Form{slashLine, nestedBlock, dquote, char,
			{Kind: String, Open: "r", Fence: "#", FenceMax: maxFence, Inner: `"`, Close: `"`}}},
	{Name: "swift", Family: "c", Extensions: []string{".swift"}, Syntax: &swift,
		Forms: []Form{slashLine, nestedBlock,
			{Kind: String, Open: `"""`, Close: `"""`, Escape: '\\', Interp: `\(`, Dedent: DedentCloser, TrimFirst: true, TrimLast: true},
			interp(dquoteLine, `\(`),
			// compile tries #""" before #", whatever their order here.
			{Kind: String, Open: "#", Fence: "#", FenceMax: maxFence, Inner: `"`, Close: `"`, Outer: "#", Interp: `\#`},
			{Kind: String, Open: "#", Fence: "#", FenceMax: maxFence, Inner: `"""`, Close: `"""`, Outer: "#", Interp: `\#`,
				Dedent: DedentCloser, TrimFirst: true, TrimLast: true}}},
	{Name: "csharp", Family: "c", Extensions: []string{".cs"},
		Forms: []Form{slashLine, slashBlock, dquoteLine, char,
			{Kind: String, Open: `"""`, Fence: `"`, FenceMax: maxFence, Close: `"""`},
			{Kind: String, Open: `@"`, Close: `"`, Doubled: true},
			{Kind: String, Open: `@$"`, Close: `"`, Doubled: true}}},
	{Name: "css", Family: "c", Extensions: []string{".css"},
		Forms: []Form{slashBlock, dquoteLine, squoteLine}},

	// Hash.
	{Name: "python", Family: "hash", Extensions: []string{".py", ".pyi"}, Syntax: &python,
		Forms: append([]Form{hashLine, dtriple, striple, dquoteLine, squoteLine}, fstrings()...)},
	{Name: "shell", Family: "hash", Extensions: []string{".sh", ".bash", ".zsh", ".ksh"}, Escape: '\\',
		Forms: []Form{{Kind: Comment, Open: "#", After: " \t;&|()"},
			{Kind: String, Open: "$'", Close: "'", Escape: '\\'},
			{Kind: String, Open: "'", Close: "'"}, dquote}},
	{Name: "ruby", Family: "hash", Extensions: []string{".rb", ".rake", ".gemspec"}, Files: []string{"Rakefile", "Gemfile"}, Syntax: &ruby,
		Forms: append([]Form{hashLine, {Kind: Comment, Open: "=begin", Close: "=end", LineStart: true},
			{Kind: Text, Open: "__END__", LineStart: true, ToEnd: true}, // data, not code
			interp(dquote, "#{"), quoteOnly(squote), {Kind: String, Open: "`", Close: "`", Escape: '\\', Computed: true, Interp: "#{"},
			interp(regexLines, "#{"),
			heredoc("<<~", "", true, false, DedentCommon, "#{"),
			heredoc("<<-", "", true, false, DedentNone, "#{"),
			heredoc("<<", upper, false, false, DedentNone, "#{")}, percents()...)},
	{Name: "elixir", Family: "hash", Extensions: []string{".ex", ".exs"}, Syntax: &elixir,
		Forms: append([]Form{hashLine,
			{Kind: String, Open: `"""`, Close: `"""`, Escape: '\\', Interp: "#{", Dedent: DedentCloser, TrimFirst: true},
			{Kind: String, Open: "'''", Close: "'''", Escape: '\\', Interp: "#{", Dedent: DedentCloser, TrimFirst: true},
			interp(dquote, "#{"), interp(squote, "#{")}, sigils()...)},
	{Name: "perl", Family: "hash", Extensions: []string{".pl", ".pm"},
		Forms: []Form{{Kind: Comment, Open: "#", After: " \t;{}()"},
			pod("=pod"), pod("=head"), pod("=over"), pod("=item"), pod("=begin"), pod("=for"), pod("=encoding"),
			dquote, squote}},
	{Name: "yaml", Family: "hash", Extensions: []string{".yaml", ".yml"},
		Forms: []Form{{Kind: Comment, Open: "#", After: " \t"},
			{Kind: String, Open: "|", After: " \t", Indented: true},
			{Kind: String, Open: ">", After: " \t", Indented: true},
			{Kind: String, Open: "'", Close: "'", Doubled: true, After: " \t[{,"},
			{Kind: String, Open: `"`, Close: `"`, Escape: '\\', After: " \t[{,"}}},
	{Name: "toml", Family: "hash", Extensions: []string{".toml"},
		Forms: []Form{hashLine, dtriple, stripleRaw, dquoteLine, {Kind: String, Open: "'", Close: "'", Line: true}}},
	{Name: "dockerfile", Family: "hash", Extensions: []string{".dockerfile"}, Files: []string{"Dockerfile", "Containerfile"},
		Forms: []Form{{Kind: Comment, Open: "#", LineStart: true}}},
	{Name: "makefile", Family: "hash", Extensions: []string{".mk", ".mak"}, Files: []string{"Makefile", "makefile", "GNUmakefile"}, Escape: '\\',
		Forms: []Form{hashLine, dquoteLine, squoteLine}},
	{Name: "hcl", Family: "hash", Extensions: []string{".tf", ".tfvars", ".hcl"},
		Forms: []Form{hashLine, slashLine, slashBlock, dquoteLine}},

	// SQL and Lua.
	// Standard SQL, with PostgreSQL's E'...' and dollar quoting and the
	// quoted names of MySQL and SQL Server.
	{Name: "sql", Family: "dash", Extensions: []string{".sql"},
		Forms: []Form{dashLine, slashBlock,
			{Kind: String, Open: "'", Close: "'", Doubled: true},
			{Kind: String, Open: "E'", Close: "'", Escape: '\\', Doubled: true, After: " \t(,="},
			{Kind: String, Open: "e'", Close: "'", Escape: '\\', Doubled: true, After: " \t(,="},
			{Kind: String, Open: "$", Fence: sqlTag, FenceMax: maxTag, Inner: "$", Close: "$", Outer: "$"},
			{Kind: Name, Open: `"`, Close: `"`, Doubled: true},
			{Kind: Name, Open: "`", Close: "`", Doubled: true},
			{Kind: Name, Open: "[", Close: "]"}}},
	{Name: "lua", Family: "dash", Extensions: []string{".lua", ".rockspec"}, Syntax: &lua,
		Forms: []Form{dashLine,
			{Kind: Comment, Open: "--[", Fence: "=", FenceMax: maxFence, Inner: "[", Close: "]", Outer: "]"},
			{Kind: String, Open: "[", Fence: "=", FenceMax: maxFence, Inner: "[", Close: "]", Outer: "]", TrimFirst: true},
			dquoteLine, squoteLine}},

	// HTML and XML.
	{Name: "html", Family: "markup", Extensions: []string{".html", ".htm", ".xhtml"},
		Forms: []Form{markupBlock, attrDquote, attrSquote}},
	{Name: "xml", Family: "markup", Extensions: []string{".xml", ".xsd", ".xsl", ".xslt", ".svg", ".plist", ".csproj", ".props", ".targets", ".xaml"},
		Forms: []Form{markupBlock, attrDquote, attrSquote, {Kind: String, Open: "<![CDATA[", Close: "]]>"}}},
}

// sqlTag are the bytes of a PostgreSQL dollar quote's tag.
const sqlTag = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_"

// pod is a block of Perl's documentation, which opens with the command
// open at the start of a line and ends with =cut there.
func pod(open string) Form {
	return Form{Kind: Comment, Open: open, Close: "=cut", LineStart: true}
}

// regex is a regular expression literal, /.../, which opens only where a
// value starts, holds classes such as [/], and is no string's text.
var regex = Form{Kind: String, Open: "/", Close: "/", Escape: '\\', Line: true, Before: "=(,:[!&|?{};~", Classes: true, Computed: true,
	Words: []string{"return", "typeof", "case", "do", "else", "in", "void", "yield", "delete", "throw", "new"}}

// regexLines is a regular expression that may run over lines, as Ruby's
// and Groovy's do, as one written with Ruby's x flag. It opens after an
// operator too, where / cannot divide, and after Ruby's and Groovy's words
// that a value follows, but not after the : of a Ruby symbol.
var regexLines = func() Form {
	f := regex
	f.Line = false
	f.Before += "+-*^%"
	f.Words, f.Calls = valueWords, callWords
	f.Glued, f.LineStarts = ":?", true
	return f
}()

// valueWords are the words of Ruby and Groovy after which a value starts.
// callWords are method names that may stand before one, and may also be a
// variable's name, so a / after one opens a regex only as Form.Calls says.
var (
	valueWords = []string{"if", "unless", "elsif", "when", "while", "until", "and", "or", "not", "return", "then",
		"case", "in", "else"}
	callWords = []string{"p", "puts", "print", "assert"}
)

// percents are Ruby's percent literals: % and a letter, or % alone, then a
// body between brackets that nest, or between one of | ! /. %r is a
// regular expression and %w, %W, %i, %I and %s are arrays and symbols,
// each computed; %q is a string like '...', and %Q and % one like "...".
// Each opens where a value starts, as a regular expression does, or after
// an operator, so a % after a value is the operator.
func percents() []Form {
	var out []Form
	for _, l := range []string{"", "q", "Q", "r", "w", "W", "i", "I", "s"} {
		for _, d := range [][2]byte{{'(', ')'}, {'[', ']'}, {'{', '}'}, {'<', '>'}, {'|', '|'}, {'!', '!'}, {'/', '/'}} {
			f := Form{Kind: String, Open: "%" + l + string(d[0]), Close: string(d[1]), Escape: '\\', Before: regex.Before + "+-*<>^",
				Words: valueWords, Calls: callWords, Glued: ":?", LineStarts: true}
			if d[0] != d[1] {
				f.Pair = d[0]
			}
			switch l {
			case "", "Q":
				f.Interp = "#{"
			case "q":
				f.QuoteEscapes = true
			case "r", "W", "I":
				f.Interp, f.Computed = "#{", true
			default:
				f.Computed = true
			}
			out = append(out, f)
		}
	}
	return out
}

// withCode is f with the openers of code inside it.
func withCode(f Form, code ...string) Form {
	f.Code = code
	return f
}

// interpolators are Scala's interpolated strings, s"...", f"...", raw"..."
// and their triple quoted forms, whose ${...} and $name are code.
func interpolators() []Form {
	var out []Form
	for _, p := range []string{"s", "f", "raw"} {
		triple := dtripleGreedy
		triple.Open = p + `"""`
		line := dquoteLine
		line.Open = p + `"`
		out = append(out, interp(triple, "$"), interp(line, "$"))
	}
	return out
}

// fstrings are Python's f-strings, with the prefix in any case, and raw:
// code inside {...}, where {{ is a brace.
func fstrings() []Form {
	var out []Form
	for _, p := range []string{"f", "F", "rf", "rF", "Rf", "RF", "fr", "fR", "Fr", "FR"} {
		for _, q := range []string{`"""`, "'''", `"`, "'"} {
			out = append(out, Form{Kind: String, Open: p + q, Close: q, Escape: '\\', Interp: "{", Spec: ":!", Line: len(q) == 1})
		}
	}
	return out
}

// interp is f, a string form, with code inside it opened by open.
func interp(f Form, open string) Form {
	f.Interp = open
	return f
}

// quoteOnly is f, a string form whose escape stands only before itself and
// the closer.
func quoteOnly(f Form) Form {
	f.QuoteEscapes = true
	return f
}

// heredoc is a heredoc form that opens with open, its body read with the
// escapes of C and code inside it opened by in, unless its tag is in single
// quotes.
func heredoc(open, first string, indent, trail bool, dedent Dedent, in string) Form {
	return Form{Kind: String, Open: open, Heredoc: true, TagFirst: first, Indent: indent, Trail: trail,
		Escape: '\\', Interp: in, Dedent: dedent}
}

// trimLast is f with the line feed that ends its body left out.
func trimLast(f Form) Form {
	f.TrimLast = true
	return f
}

// upper are the bytes a tag of Ruby's <<TAG may start with, which tell it
// from a shift: an upper case letter or _.
const upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ_"

// lower are the lower case ASCII letters.
const lower = "abcdefghijklmnopqrstuvwxyz"

// sigils are Elixir's sigils: ~ and a letter, then a body between one of
// eight pairs of delimiters, or three quotes over lines. A lower case
// sigil reads escapes and #{...}; an upper case one, which may run to more
// letters, reads neither.
func sigils() []Form {
	var out []Form
	for _, d := range [][2]string{{`"""`, `"""`}, {"'''", "'''"}, {"(", ")"}, {"[", "]"}, {"{", "}"}, {"<", ">"},
		{`"`, `"`}, {"'", "'"}, {"|", "|"}, {"/", "/"}} {
		out = append(out,
			Form{Kind: String, Open: "~", Tag: lower, TagMax: 1, Inner: d[0], Close: d[1], Escape: '\\', Interp: "#{"},
			Form{Kind: String, Open: "~", Tag: upper[:26], TagMax: 8, Inner: d[0], Close: d[1]})
	}
	return out
}
