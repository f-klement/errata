package lexer

// Syntax is how a language writes literal data: the words and the
// punctuation the literal reader reads a file's statements and values by.
// The lexer reads none of it; it sits in the row so that adding a language
// is one row. Each field is a fact of the language, not a rule of the
// reader.
type Syntax struct {
	// Declare are the words that may stand before an assigned name, such
	// as const, let, val or local.
	Declare []string
	// Keywords are the words that start a statement which assigns no name
	// and calls nothing: if, def, class, import, and the like.
	Keywords []string
	// Rebind are the words after which a name is bound anew, such as def,
	// class, import or for: a name that follows one is computed.
	Rebind []string
	// Mutators are the functions that change the name they are handed
	// first, as Lua's table.insert(deps, x) changes deps.
	Mutators []string
	// Assign are the operators that assign a value to a name; = when none.
	Assign []string
	// Annotate reads a type between a name and its = (name: Type = value).
	Annotate bool
	// Sigil is the byte a variable's name starts with, which the name read
	// leaves out: PHP's $.
	Sigil byte
	// Entry are what separates a mapping entry's key from its value, such
	// as : or =>.
	Entry []string
	// NameKeys are the entry separators after which a bare name is the key
	// itself, as in JavaScript's {a: 1} or Lua's {a = 1}, rather than a
	// variable.
	NameKeys []string
	// Keyword are what separates a keyword argument's name from its value
	// in a call, such as = in Python or : in Swift.
	Keyword []string
	// True, False and Null are the words of those values.
	True, False, Null []string
	// FoldWords reads True, False and Null in any case, as PHP does.
	FoldWords bool
	// Brackets are the pairs that hold a list or a mapping.
	Brackets []Bracket
	// ArrayCall is a word followed by ( that is a bracket of Brackets'
	// first pair, as PHP's array(...).
	ArrayCall string
	// Command reads a call without parentheses, as Ruby's gem "rails".
	Command bool
	// Symbols reads :name as the literal name, as Ruby and Elixir write
	// symbols and atoms.
	Symbols bool
	// Aliases reads a capitalised dotted name as literal text, as Elixir's
	// MyApp.Repo is an atom.
	Aliases bool
	// Members reads .name as literal text, as Swift's implicit member
	// .v13 names a case of the type the context gives.
	Members bool
	// Sigils are the letters of a Tag form whose body is a string; any
	// other letter's is computed, as Elixir's ~r is a regular expression.
	Sigils string
	// Adjacent joins two strings that stand next to each other into one,
	// as Python and Ruby do.
	Adjacent bool
	// Prefixes are the words a string may be written straight after.
	Prefixes []Prefix
	// Escapes are the bytes after a backslash that the language's strings
	// decode: the control characters' letters, the quotes, 0 alone, x, u
	// for four hex digits, U for eight, { for \u{...}, s for a space, z for
	// Lua's skip, and a line feed for a joined line. Any other escape makes
	// a string computed.
	Escapes string
	// Indent makes a statement that starts further in than the line's
	// first column part of a block, as Python's indentation does.
	Indent bool
	// Blocks are the words that open a block that End closes, as Ruby's do
	// and Lua's function; Loops open one and take the next Do on their
	// line as theirs; Lead open one only where a statement or an
	// expression starts, as Ruby's if, which after a value is a modifier.
	Blocks, Loops, Lead []string
	Do, End             string
	// Until closes a block Repeat opens, as Lua's repeat ... until.
	Repeat, Until string
	// Return is the word that makes a file's value, as PHP's return [...].
	Return string
	// Semicolons ends a statement only at ;, as PHP does; otherwise a line
	// end does too, unless the next line goes on with an operator.
	Semicolons bool
	// Casts are the words after a value that start a type the value is
	// read as, as TypeScript's as const.
	Casts []string
	// Default is the word after Declare that names a value, as export
	// default does in JavaScript.
	Default string
}

// Bracket is a pair of brackets that holds a list, a mapping, or either.
type Bracket struct {
	Open, Close byte
	// Pre is a byte the opener is written after, as Elixir's %{.
	Pre byte
	// Items and Entries are whether the pair holds a list and a mapping.
	Items, Entries bool
	// Map makes an empty pair a mapping; otherwise it is a list.
	Map bool
}

// Prefix is a word a string may be written straight after, such as
// Python's r or f.
type Prefix struct {
	Word string
	// Fold reads the word in any case.
	Fold bool
	// Raw reads the body with no escape.
	Raw bool
	// Interp opens code inside the body, as Form.Interp does.
	Interp string
	// Bytes makes the string a bytes literal, which decodes no \u or \U.
	Bytes bool
}
