package lexer

// The syntax of each row the literal reader reads (Syntax).

// list, tuple and mapping brackets several rows share.
var (
	squareItems   = Bracket{Open: '[', Close: ']', Items: true}
	squareEither  = Bracket{Open: '[', Close: ']', Items: true, Entries: true}
	parenItems    = Bracket{Open: '(', Close: ')', Items: true}
	braceEntries  = Bracket{Open: '{', Close: '}', Entries: true, Map: true}
	trueFalseNull = struct{ t, f, n []string }{[]string{"true"}, []string{"false"}, []string{"null"}}
)

var python = Syntax{
	Keywords: []string{"if", "elif", "else", "for", "while", "def", "class", "import", "from", "try", "except",
		"finally", "with", "return", "raise", "pass", "del", "global", "nonlocal", "assert", "async", "await",
		"lambda", "yield", "break", "continue", "match", "case", "type"},
	Rebind:   []string{"def", "class", "import", "from", "for", "as", "global", "nonlocal", "del", "case"},
	Annotate: true,
	Entry:    []string{":"},
	Keyword:  []string{"="},
	True:     []string{"True"}, False: []string{"False"}, Null: []string{"None"},
	Brackets: []Bracket{squareItems, parenItems, {Open: '{', Close: '}', Items: true, Entries: true, Map: true}},
	Adjacent: true,
	Prefixes: []Prefix{{Word: "r", Fold: true, Raw: true}, {Word: "b", Fold: true, Bytes: true}, {Word: "u", Fold: true},
		{Word: "rb", Fold: true, Raw: true, Bytes: true}, {Word: "br", Fold: true, Raw: true, Bytes: true}},
	// An f-string is a form of its own in the row, whose code the lexer walks.
	Indent:      true,
	Classes:     []string{"class"},
	Annotations: true,
	Escapes:     "\\'\"abfnrtv0xuU\n",
	Selves:      []string{"self", "cls"},
	Methods:     []string{"append", "extend", "insert", "remove", "pop", "popitem", "clear", "update", "setdefault", "sort", "reverse", "add", "discard"},
}

var ruby = Syntax{
	Keywords: []string{"if", "unless", "while", "until", "for", "case", "when", "def", "class", "module", "begin",
		"rescue", "ensure", "return", "yield", "alias", "undef", "then", "else", "elsif", "do", "end", "break",
		"next", "redo", "retry", "and", "or", "not", "BEGIN", "END"},
	Rebind:   []string{"def", "class", "module", "for", "alias"},
	Entry:    []string{"=>", ":"},
	NameKeys: []string{":"},
	Keyword:  []string{":"},
	True:     []string{"true"}, False: []string{"false"}, Null: []string{"nil"},
	Brackets: []Bracket{squareItems, braceEntries},
	Command:  true,
	Symbols:  true,
	Adjacent: true,
	Blocks:   []string{"do", "def", "class", "module", "begin", "case", "for"},
	Lead:     []string{"if", "unless", "while", "until"},
	Loops:    []string{"while", "until", "for"},
	Do:       "do", End: "end",
	Classes: []string{"class", "module"},
	Escapes: "\\'\"abefnrstv0xu{#\n",
	Selves:  []string{"self"},
	Frozen:  "freeze",
	Methods: []string{"push", "pop", "shift", "unshift", "concat", "insert", "delete", "clear", "replace", "merge!", "update", "store", "sort!", "map!", "reverse!"},
}

var javascript = Syntax{
	Declare: []string{"export", "const", "let", "var"},
	Keywords: []string{"if", "else", "for", "while", "do", "switch", "function", "class", "import", "return",
		"throw", "try", "catch", "finally", "async", "await", "new", "delete", "typeof", "void", "yield", "break",
		"continue", "with"},
	Rebind:   []string{"function", "class", "import", "const", "let", "var", "of", "in", "as", "catch"},
	Entry:    []string{":"},
	NameKeys: []string{":"},
	True:     trueFalseNull.t, False: trueFalseNull.f, Null: []string{"null", "undefined"},
	Brackets:  []Bracket{squareItems, braceEntries},
	Default:   "default",
	Classes:   []string{"class"},
	Modifiers: []string{"static"},
	Constants: []string{"const"},
	Escapes:   "\\'\"bfnrtv0xu{`$\n",
	Selves:    []string{"this"},
	Methods:   []string{"push", "pop", "shift", "unshift", "splice", "sort", "reverse", "fill", "copyWithin", "set", "delete", "add", "clear"},
}

// typescript is javascript with type annotations, the words of types, and
// as and satisfies after a value.
var typescript = func() Syntax {
	s := javascript
	s.Declare = []string{"export", "const", "let", "var", "declare"}
	s.Keywords = append([]string{"interface", "type", "enum", "namespace", "abstract"}, javascript.Keywords...)
	s.Rebind = append([]string{"interface", "type", "enum", "namespace"}, javascript.Rebind...)
	s.Annotate = true
	s.Casts = []string{"as", "satisfies"}
	s.Modifiers = []string{"static", "readonly", "public", "private", "protected", "override", "abstract"}
	s.Constants = []string{"const", "readonly"}
	s.Annotations = true
	return s
}()

var php = Syntax{
	Declare: []string{"const"},
	Keywords: []string{"if", "else", "elseif", "for", "foreach", "while", "do", "switch", "match", "function", "fn",
		"class", "interface", "trait", "enum", "namespace", "use", "echo", "print", "require", "require_once",
		"include", "include_once", "declare", "abstract", "return", "final", "static", "public", "private", "protected",
		"new", "throw", "try", "catch", "finally", "global", "unset", "goto", "break", "continue"},
	Rebind:  []string{"function", "fn", "class", "interface", "trait", "enum", "as", "global", "static", "use", "catch"},
	Sigil:   '$',
	Entry:   []string{"=>"},
	Keyword: []string{":"},
	True:    trueFalseNull.t, False: trueFalseNull.f, Null: trueFalseNull.n,
	FoldWords:  true,
	Brackets:   []Bracket{squareEither},
	ArrayCall:  "array",
	Return:     "return",
	Semicolons: true,
	Escapes:    "\\\"$efnrtv0x{",
	Classes:    []string{"class", "interface", "trait", "enum"},
	Modifiers:  []string{"public", "private", "protected", "static", "final", "readonly", "var", "abstract"},
	Constants:  []string{"const"},
	Typed:      true,
	Selves:     []string{"this", "self", "static"},
	Methods:    []string{"append", "offsetSet", "offsetUnset", "exchangeArray"},
}

var groovy = Syntax{
	Declare: []string{"def", "final", "static", "public", "private", "protected"},
	Keywords: []string{"if", "else", "for", "while", "do", "switch", "class", "interface", "trait", "enum",
		"import", "package", "return", "try", "catch", "finally", "throw", "new", "assert", "break", "continue"},
	Rebind:   []string{"def", "class", "interface", "trait", "enum", "import", "for", "in", "catch"},
	Entry:    []string{":"},
	NameKeys: []string{":"},
	Keyword:  []string{":"},
	True:     trueFalseNull.t, False: trueFalseNull.f, Null: trueFalseNull.n,
	Brackets:    []Bracket{squareEither},
	Command:     true,
	Escapes:     "\\'\"bfnrt$u",
	Classes:     []string{"class", "interface", "enum", "trait"},
	Modifiers:   []string{"abstract"},
	Constants:   []string{"final"},
	Typed:       true,
	Annotations: true,
	Selves:      []string{"this"},
	Methods:     []string{"add", "addAll", "put", "putAll", "remove", "removeAll", "retainAll", "clear", "set", "sort", "push", "pop", "leftShift"},
}

var kotlin = Syntax{
	Declare: []string{"val", "var", "const", "private", "internal", "public", "lateinit"},
	Keywords: []string{"fun", "class", "object", "interface", "if", "else", "when", "for", "while", "do", "import",
		"package", "return", "try", "catch", "finally", "throw", "typealias", "enum", "data", "sealed", "annotation",
		"abstract", "open", "override", "break", "continue"},
	Rebind:   []string{"fun", "class", "object", "interface", "for", "import", "val", "var", "typealias", "catch"},
	Annotate: true,
	Keyword:  []string{"="},
	True:     trueFalseNull.t, False: trueFalseNull.f, Null: trueFalseNull.n,
	Brackets: []Bracket{squareItems},
	Escapes:  "\\'\"bnrt$u",
	Classes:  []string{"object", "class", "interface"},
	Modifiers: []string{"companion", "data", "enum", "sealed", "abstract", "open", "override", "protected",
		"annotation", "inner", "final"},
	Constants:   []string{"const", "val"},
	Annotations: true,
	Selves:      []string{"this"},
	Methods:     []string{"add", "addAll", "put", "putAll", "remove", "removeAll", "removeAt", "retainAll", "clear", "set", "sort", "append"},
}

var scala = Syntax{
	Declare: []string{"val", "var", "lazy", "final", "private", "protected", "implicit", "override"},
	Keywords: []string{"def", "class", "object", "trait", "if", "else", "match", "for", "while", "do", "import",
		"package", "return", "try", "catch", "finally", "throw", "new", "case", "type", "given", "using",
		"extension", "enum", "abstract", "sealed"},
	Rebind:   []string{"def", "class", "object", "trait", "for", "import", "val", "var", "type", "given", "case"},
	Assign:   []string{"=", ":="},
	Annotate: true,
	Keyword:  []string{"="},
	True:     trueFalseNull.t, False: trueFalseNull.f, Null: trueFalseNull.n,
	Brackets: []Bracket{parenItems},
	// s, f and raw are forms of the row, whose code the lexer walks.
	Escapes:     "\\'\"bfnrtu",
	Classes:     []string{"object", "class", "trait"},
	Modifiers:   []string{"case", "sealed", "abstract"},
	Constants:   []string{"val"},
	Annotations: true,
	Selves:      []string{"this"},
	Methods:     []string{"append", "prepend", "update", "clear", "remove", "insert", "addOne", "subtractOne", "put"},
}

var swift = Syntax{
	Declare: []string{"let", "var", "public", "private", "internal", "fileprivate", "static"},
	Keywords: []string{"func", "class", "struct", "enum", "protocol", "extension", "import", "if", "guard", "else",
		"for", "while", "switch", "return", "repeat", "do", "throw", "defer", "typealias", "init", "break",
		"continue"},
	Rebind:   []string{"func", "class", "struct", "enum", "protocol", "for", "import", "let", "var", "typealias", "catch"},
	Annotate: true,
	Entry:    []string{":"},
	Keyword:  []string{":"},
	True:     trueFalseNull.t, False: trueFalseNull.f, Null: []string{"nil"},
	Brackets:    []Bracket{squareEither, parenItems},
	Members:     true,
	Escapes:     "\\'\"0nrt{",
	Classes:     []string{"class", "struct", "enum"},
	Modifiers:   []string{"final", "open"},
	Constants:   []string{"let"},
	Annotations: true,
	Selves:      []string{"self"},
	Methods:     []string{"append", "insert", "remove", "removeAll", "removeFirst", "removeLast", "sort", "reverse", "updateValue", "removeValue"},
}

var elixir = Syntax{
	Keywords: []string{"def", "defp", "defmodule", "defmacro", "defmacrop", "defstruct", "defprotocol", "defimpl",
		"import", "alias", "require", "use", "if", "unless", "case", "cond", "with", "for", "fn", "quote",
		"receive", "try", "do", "end", "else", "after", "rescue", "catch"},
	Rebind:   []string{"def", "defp", "defmodule", "defmacro", "defmacrop", "for", "alias", "import", "fn"},
	Entry:    []string{"=>", ":"},
	NameKeys: []string{":"},
	Keyword:  []string{":"},
	True:     trueFalseNull.t, False: trueFalseNull.f, Null: []string{"nil"},
	Brackets: []Bracket{squareEither, {Open: '{', Close: '}', Items: true}, {Open: '{', Close: '}', Pre: '%', Entries: true, Map: true}},
	Command:  true,
	Symbols:  true,
	Aliases:  true,
	Sigils:   "sS",
	Blocks:   []string{"do", "fn"},
	End:      "end",
	Escapes:  "\\'\"abefnrstv0xu{#",
}

var lua = Syntax{
	Declare: []string{"local"},
	Keywords: []string{"if", "for", "while", "repeat", "function", "do", "end", "then", "else", "elseif", "until",
		"break", "goto", "not", "and", "or", "in", "return"},
	Rebind:   []string{"function", "for", "local", "in"},
	Mutators: []string{"table.insert", "table.remove", "table.sort", "rawset"},
	Entry:    []string{"="},
	NameKeys: []string{"="},
	True:     trueFalseNull.t, False: trueFalseNull.f, Null: []string{"nil"},
	Brackets: []Bracket{{Open: '{', Close: '}', Items: true, Entries: true}},
	Command:  true,
	Blocks:   []string{"function", "do", "if"},
	Loops:    []string{"while", "for"},
	Do:       "do", End: "end",
	Repeat: "repeat", Until: "until",
	Return:  "return",
	Escapes: "\\'\"abfnrtvxz{",
}

// java writes every value in a class: a field, whose type stands before its
// name. Only the code reader reads it.
var java = Syntax{
	Keywords: []string{"if", "else", "for", "while", "do", "switch", "class", "interface", "enum", "record",
		"import", "package", "return", "try", "catch", "finally", "throw", "new", "assert", "break", "continue",
		"synchronized"},
	Rebind:  []string{"class", "interface", "enum", "record", "import", "for", "catch"},
	Keyword: []string{"="},
	True:    trueFalseNull.t, False: trueFalseNull.f, Null: trueFalseNull.n,
	Brackets:       []Bracket{{Open: '{', Close: '}', Items: true}},
	Semicolons:     true,
	Escapes:        "\\'\"bfnrts0u",
	Classes:        []string{"class", "interface", "enum", "record"},
	Modifiers:      []string{"public", "private", "protected", "static", "final", "transient", "volatile", "abstract", "sealed"},
	Constants:      []string{"final"},
	Typed:          true,
	Annotations:    true,
	InClasses:      true,
	Selves:         []string{"this"},
	ConstantBodies: []string{"interface"},
	Methods:        []string{"add", "addAll", "put", "putAll", "putIfAbsent", "remove", "removeAll", "retainAll", "clear", "set", "sort", "replaceAll", "push", "pop", "offer", "poll", "append", "insert", "delete", "setLength", "compute", "merge"},
}
