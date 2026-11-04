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
	{Name: "kotlin", Family: "c", Extensions: []string{".kt", ".kts"},
		Forms: []Form{slashLine, nestedBlock, dtripleGreedy, dquoteLine, char}},
	{Name: "javascript", Family: "c", Extensions: []string{".js", ".mjs", ".cjs", ".jsx"},
		Forms: []Form{slashLine, slashBlock, dquoteLine, squoteLine, backtick}},
	{Name: "typescript", Family: "c", Extensions: []string{".ts", ".mts", ".cts", ".tsx"},
		Forms: []Form{slashLine, slashBlock, dquoteLine, squoteLine, backtick}},
	{Name: "rust", Family: "c", Extensions: []string{".rs"},
		Forms: []Form{slashLine, nestedBlock, dquote, char,
			{Kind: String, Open: "r", Fence: "#", FenceMax: maxFence, Inner: `"`, Close: `"`}}},
	{Name: "swift", Family: "c", Extensions: []string{".swift"},
		Forms: []Form{slashLine, nestedBlock, dtriple, dquoteLine,
			// compile tries #""" before #", whatever their order here.
			{Kind: String, Open: "#", Fence: "#", FenceMax: maxFence, Inner: `"`, Close: `"`, Outer: "#"},
			{Kind: String, Open: "#", Fence: "#", FenceMax: maxFence, Inner: `"""`, Close: `"""`, Outer: "#"}}},
	{Name: "csharp", Family: "c", Extensions: []string{".cs"},
		Forms: []Form{slashLine, slashBlock, dquoteLine, char,
			{Kind: String, Open: `"""`, Fence: `"`, FenceMax: maxFence, Close: `"""`},
			{Kind: String, Open: `@"`, Close: `"`, Doubled: true},
			{Kind: String, Open: `@$"`, Close: `"`, Doubled: true}}},
	{Name: "css", Family: "c", Extensions: []string{".css"},
		Forms: []Form{slashBlock, dquoteLine, squoteLine}},

	// Hash.
	{Name: "python", Family: "hash", Extensions: []string{".py", ".pyi"},
		Forms: []Form{hashLine, dtriple, striple, dquoteLine, squoteLine}},
	{Name: "shell", Family: "hash", Extensions: []string{".sh", ".bash", ".zsh", ".ksh"}, Escape: '\\',
		Forms: []Form{{Kind: Comment, Open: "#", After: " \t;&|()"},
			{Kind: String, Open: "$'", Close: "'", Escape: '\\'},
			{Kind: String, Open: "'", Close: "'"}, dquote}},
	{Name: "ruby", Family: "hash", Extensions: []string{".rb", ".rake", ".gemspec"}, Files: []string{"Rakefile", "Gemfile"},
		Forms: []Form{hashLine, {Kind: Comment, Open: "=begin", Close: "=end", LineStart: true},
			dquote, squote, backtick}},
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
	{Name: "lua", Family: "dash", Extensions: []string{".lua"},
		Forms: []Form{dashLine,
			{Kind: Comment, Open: "--[", Fence: "=", FenceMax: maxFence, Inner: "[", Close: "]", Outer: "]"},
			{Kind: String, Open: "[", Fence: "=", FenceMax: maxFence, Inner: "[", Close: "]", Outer: "]"},
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
