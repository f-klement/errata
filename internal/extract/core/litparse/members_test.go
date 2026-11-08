package litparse

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
)

// member is what ParseMembers reads under name, a dotted name, in src, a
// file of lang: text@line, after a ~ when computed and before a ! when a
// Constants word declared it; "none" when nothing is there, "mapping" or
// "sequence" for a node that is no scalar, or "error: " and the message.
func member(t *testing.T, lang, src, name string) string {
	t.Helper()
	l, ok := core.LanguageNamed(lang)
	if !ok {
		t.Fatalf("no language %s", lang)
	}
	tree, err := ParseMembers("f", l, []byte(src))
	if err != nil {
		return "error: " + err.Error()
	}
	n, constant := tree.Root, false
	for part := range strings.SplitSeq(name, ".") {
		if n.Shape != core.Mapping || n.Field[part] == nil {
			return "none"
		}
		n, constant = n.Field[part], tree.Const(n, part)
	}
	if n.Shape != core.Scalar {
		return n.Shape.String()
	}
	out := n.Text + "@" + strconv.Itoa(n.Line)
	if n.NotLiteral {
		out = "~" + out
	}
	if constant {
		out = "!" + out
	}
	return out
}

func members(t *testing.T, cs []struct{ lang, src, name, want string }) {
	t.Helper()
	for _, c := range cs {
		if got := member(t, c.lang, c.src, c.name); got != c.want {
			t.Errorf("%s %q at %s:\n got %s\nwant %s", c.lang, c.src, c.name, got, c.want)
		}
	}
}

// A class's attributes, an object's members and a class's static final
// fields are read under the class's name, in each language. Planted
// breaks: class not called; the members put in the scope around; Typed
// not read; Modifiers not skipped.
func TestClassMembers(t *testing.T) {
	members(t, []struct{ lang, src, name, want string }{
		{"python", "class Config:\n    DEBUG = False\n    PORT: int = 8080\n\n    def f(self):\n        PORT = 1\n\nX = 1\n", "Config.PORT", "~8080@3"},
		{"python", "class Config:\n    DEBUG = False\n    PORT: int = 8080\n\n    def f(self):\n        return 2\n\nX = 1\n", "Config.PORT", "8080@3"},
		{"python", "class Config:\n    DEBUG = False\n    PORT: int = 8080\n\n    def f(self):\n        return 2\n\nX = 1\n", "X", "1@8"},
		{"python", "@dataclass(frozen=True)\nclass Config(Base):\n    NAME = 'a'\n", "Config.NAME", "a@3"},
		{"python", "class A:\n    class B:\n        V = '2'\n    W = 3\n", "A.B.V", "2@3"},
		{"python", "class A:\n    class B:\n        V = '2'\n    W = 3\n", "A.W", "3@4"},
		{"python", "class A: X = 1\n", "A", "none"},
		{"ruby", "module App\n  VERSION = '1.2.0'\n  class Config\n    PORT = 80\n    def x; end\n  end\nend\nY = 1\n", "App.VERSION", "1.2.0@2"},
		{"ruby", "module App\n  VERSION = '1.2.0'\n  class Config\n    PORT = 80\n    def x; end\n  end\nend\nY = 1\n", "App.Config.PORT", "80@4"},
		{"ruby", "module App\n  VERSION = '1.2.0'\n  class Config\n    PORT = 80\n    def x; end\n  end\nend\nY = 1\n", "Y", "1@8"},
		{"ruby", "class A\n  class << self\n    X = 1\n  end\n  Y = 2\nend\n", "A.Y", "2@5"},
		{"ruby", "class A\n  attr_reader :x\nend\n", "A.attr_reader", "none"},
		{"javascript", "export class Config {\n  static PORT = 8080\n  name = 'x'\n  get(a) {\n    return a\n  }\n}\n", "Config.PORT", "8080@2"},
		{"javascript", "class A { static B = 'b' }\nconst C = 1\n", "A.B", "b@1"},
		{"javascript", "class A { static B = 'b' }\nconst C = 1\n", "C", "!1@2"},
		{"javascript", "class A {\n  get(a) { return a }\n}\n", "A.get", "none"},
		{"typescript", "export class Config {\n  @Input() name = 'n'\n  private static readonly PORT: number = 8080;\n}\n", "Config.PORT", "!8080@3"},
		{"typescript", "export class Config {\n  @Input() name = 'n'\n  private static readonly PORT: number = 8080;\n}\n", "Config.name", "n@2"},
		{"php", "<?php\nfinal class Config\n{\n    public const VERSION = '2.1';\n    public static $port = 80;\n    private ?int $x = null;\n    public function f(): int { $y = 1; return 1; }\n}\n$z = 1;\n", "Config.VERSION", "!2.1@4"},
		{"php", "<?php\nfinal class Config\n{\n    public const VERSION = '2.1';\n    public static $port = 80;\n    private ?int $x = null;\n    public function f(): int { $y = 1; return 1; }\n}\n$z = 1;\n", "Config.port", "80@5"},
		{"php", "<?php\nfinal class Config\n{\n    public const VERSION = '2.1';\n    public static $port = 80;\n    private ?int $x = null;\n    public function f(): int { $y = 1; return 1; }\n}\n$z = 1;\n", "Config.x", "null@6"},
		{"php", "<?php\nfinal class Config\n{\n    public const VERSION = '2.1';\n    public static $port = 80;\n    private ?int $x = null;\n    public function f(): int { $y = 1; return 1; }\n}\n$z = 1;\n", "z", "1@9"},
		{"java", "package a;\n\nimport java.util.List;\n\n@SuppressWarnings(\"x\")\npublic final class Version {\n  public static final String VERSION = \"3.4.1\";\n  static final int[] PORTS = {80, 443};\n  private static final java.util.Map<String, Integer> M = java.util.Map.of();\n  int plain = 7;\n\n  @Override\n  public String toString() {\n    int local = 1;\n    return VERSION;\n  }\n\n  public static class Inner {\n    public static final long N = 5L;\n  }\n}\n", "Version.VERSION", "!3.4.1@7"},
		{"java", "public final class Version {\n  static final int[] PORTS = {80, 443};\n  int plain = 7;\n  public static class Inner {\n    public static final long N = 5L;\n  }\n}\n", "Version.PORTS", "a sequence"},
		{"java", "public final class Version {\n  static final int[] PORTS = {80, 443};\n  int plain = 7;\n  public static class Inner {\n    public static final long N = 5L;\n  }\n}\n", "Version.plain", "7@3"},
		{"java", "public final class Version {\n  static final int[] PORTS = {80, 443};\n  int plain = 7;\n  public static class Inner {\n    public static final long N = 5L;\n  }\n}\n", "Version.Inner.N", "!5L@5"},
		{"java", "class A\n{\n  static final String S = \"\"\"\n    x\n    \"\"\";\n}\n", "A.S", "!~\"\"\"\n    x\n    \"\"\"@3"},
		{"java", "interface Limits {\n  int MAX = 10;\n}\n", "Limits.MAX", "!10@2"},
		// An annotation type: @ before a word of the language is no annotation.
		// Only a rapid property reached that stop, by chance, so the coverage of
		// parser.annotations, and its CRAP score (scripts/crap), changed from
		// run to run.
		{"java", "@interface Marker {\n  int X = 1;\n}\n", "Marker.X", "!1@2"},
		{"java", "class A {\n  static int X = 1;\n  static { X = 2; }\n}\n", "A.X", "~1@2"},
		{"groovy", "class Build {\n  static final String VERSION = '1.0'\n  def name = 'b'\n}\n", "Build.VERSION", "!1.0@2"},
		{"groovy", "class Build {\n  static final String VERSION = '1.0'\n  def name = 'b'\n}\n", "Build.name", "b@3"},
		{"kotlin", "object Config {\n    const val PORT = 8080\n    var mode = \"dev\"\n}\nclass App {\n    companion object {\n        const val NAME = \"app\"\n    }\n}\n", "Config.PORT", "!8080@2"},
		{"kotlin", "object Config {\n    const val PORT = 8080\n    var mode = \"dev\"\n}\nclass App {\n    companion object {\n        const val NAME = \"app\"\n    }\n}\n", "Config.mode", "dev@3"},
		{"kotlin", "object Config {\n    const val PORT = 8080\n    var mode = \"dev\"\n}\nclass App {\n    companion object {\n        const val NAME = \"app\"\n    }\n}\n", "App.NAME", "!app@7"},
		{"kotlin", "data class P(val x: Int = 1)\nval y = 2\n", "y", "!2@2"},
		{"scala", "object Settings {\n  val Version = \"0.9\"\n  var n = 1\n}\n", "Settings.Version", "!0.9@2"},
		{"swift", "struct Config {\n    static let port = 8080\n    var name = \"x\"\n}\n", "Config.port", "!8080@2"},
		{"swift", "struct Config {\n    static let port = 8080\n    var name = \"x\"\n}\n", "Config.name", "x@3"},
	})
}

// A class the file changes from outside is computed whole, a member a
// statement in its body changes is computed, and Parse, for the literal
// reader, reads no class's members. Planted breaks: taintedSince not run
// on a body; members read by Parse.
func TestChangedMembers(t *testing.T) {
	members(t, []struct{ lang, src, name, want string }{
		{"python", "class C:\n    X = 1\n\nC.X = 2\n", "C", "~C.X = 2@4"},
		{"python", "class C:\n    X = 1\n    X += 1\n", "C.X", "~1@2"},
		{"python", "class C:\n    X = 1\n    if a:\n        X = 3\n", "C.X", "~1@2"},
		{"javascript", "class C {\n  static X = 1\n  static { C.X = 2 }\n}\n", "C", "~class C {\n  static X = 1\n  static { C.X = 2 }\n}@1"},
		{"ruby", "class C\n  X = 'a'\n  X << 'b'\nend\n", "C.X", "~'a'@2"},
		{"kotlin", "class C {\n    companion object {\n        var n = 1\n    }\n    init { n = 2 }\n}\n", "C.n", "~1@3"},
	})
	l, _ := core.LanguageNamed("python")
	root, err := Parse("f", l, []byte("class C:\n    X = 1\nY = 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := root.Field["C"]; ok || len(root.Keys) != 1 {
		t.Errorf("Parse read a class: %v", root.Keys)
	}
}

// Classes nested past MaxDepth are refused as brackets nested that deep
// are. Planted break: the depth not checked.
func TestClassesNestedPastTheLimit(t *testing.T) {
	deep := func(n int) string {
		var b strings.Builder
		for i := range n {
			b.WriteString("class A" + strconv.Itoa(i) + " {\n")
		}
		return b.String() + "static X = 1\n" + strings.Repeat("}\n", n)
	}
	if got := member(t, "javascript", deep(core.MaxDepth), "A0.A1"); got != "a mapping" {
		t.Errorf("classes nested to the limit: %.200s", got)
	}
	if got := member(t, "javascript", deep(core.MaxDepth+1), "A0"); !strings.Contains(got, "nested more than 1000 levels deep") {
		t.Errorf("classes nested past the limit: %.200s", got)
	}
}

// In Java, whose values are all in classes, an assignment planted in a
// comment, a block comment or a string is not read as a member, and the
// string is read whole. Planted break: the lexer's comments not skipped.
func TestPlantedMembersAreNotRead(t *testing.T) {
	src := "class A {\n  static int X = 1; // PLANTED = 9;\n  /* PLANTED = 9; */\n  static String S = \"PLANTED = 9;\";\n}\n"
	members(t, []struct{ lang, src, name, want string }{
		{"java", src, "A.X", "1@2"},
		{"java", src, "A.PLANTED", "none"},
		{"java", src, "A.S", "PLANTED = 9;@4"},
	})
}

// A member a method changes through this, self, cls, $this, self:: or
// static:: is computed, and no constant, as a member the class's body
// assigns by name is; so is a TypeScript readonly field a constructor
// assigns. A class exported as the default reads as one exported by name.
// A Java interface's fields are constants. Planted breaks: change not
// tainting the chain's first member; the Default word not skipped before a
// class; ConstantBodies not read.
func TestMembersChangedThroughSelf(t *testing.T) {
	members(t, []struct{ lang, src, name, want string }{
		{"typescript", "export class A {\n  readonly port = 8080\n  constructor(p: number) { this.port = p }\n}\n", "A.port", "~8080@2"},
		{"javascript", "class A {\n  static X = 1\n  static m() { this.X = 2 }\n}\n", "A.X", "~1@2"},
		{"javascript", "class A {\n  static X = 1\n  static m() { return this.X }\n}\n", "A.X", "1@2"},
		{"java", "class A {\n  int port = 1;\n  A(int p) { this.port = p; }\n}\n", "A.port", "~1@2"},
		{"java", "class A {\n  final int[] P = {1};\n  void f() { this.P[0] = 2; }\n}\n", "A.P", "~{1}@2"},
		{"php", "<?php\nclass A {\n  public static $port = 1;\n  function f() { self::$port = 2; }\n}\n", "A.port", "~1@3"},
		{"php", "<?php\nclass A {\n  public $port = 1;\n  function f() { $this->port = 2; }\n}\n", "A.port", "~1@3"},
		{"php", "<?php\nclass A {\n  public static $port = 1;\n  function f() { static::$port++; }\n}\n", "A.port", "~1@3"},
		{"kotlin", "class A {\n    var port = 1\n    fun f() { this.port = 2 }\n}\n", "A.port", "~1@2"},
		{"swift", "struct A {\n    var port = 1\n    mutating func f() { self.port = 2 }\n}\n", "A.port", "~1@2"},
		{"python", "class A:\n    PORT = 1\n    def __init__(self, p):\n        self.PORT = p\n", "A.PORT", "~1@2"},
		{"python", "class A:\n    PORT = 1\n    @classmethod\n    def f(cls):\n        cls.PORT += 1\n", "A.PORT", "~1@2"},
		{"python", "class A:\n    PORT = 1\n    def f(self):\n        return self.PORT\n", "A.PORT", "1@2"},
		{"groovy", "class A {\n  def port = 1\n  def f() { this.port = 2 }\n}\n", "A.port", "~1@2"},
		{"ruby", "class A\n  PORT = 1\n  def f\n    self.PORT = 2\n  end\nend\n", "A.PORT", "~1@2"},
		{"javascript", "class A {\n  static X = 1\n  static m(o) { o.X = 2 }\n}\n", "A.X", "1@2"},
		{"scala", "class A {\n  var port = 1\n  def f() = { this.port = 2 }\n}\n", "A.port", "~1@2"},
		{"typescript", "export default class C {\n  static readonly X = 'x'\n}\n", "C.X", "!x@2"},
		{"javascript", "export default class C {\n  static X = 'x'\n}\n", "C.X", "x@2"},
		{"javascript", "export default { a: 1 }\n", "default.a", "1@1"},
		{"java", "interface Limits {\n  int MAX = 10;\n  class Inner { int N = 2; }\n}\n", "Limits.Inner.N", "2@3"},
	})
}

// A string literal before Ruby's .freeze is the string, and only then: a
// call of anything else, or .freeze on anything but a string literal, is
// computed. Planted break: Frozen not read.
func TestRubyFreeze(t *testing.T) {
	members(t, []struct{ lang, src, name, want string }{
		{"ruby", "module App\n  VERSION = '1.2.3'.freeze\nend\n", "App.VERSION", "1.2.3@2"},
		{"ruby", "V = \"1.2\".freeze\n", "V", "1.2@1"},
		{"ruby", "V = '1.2'.dup\n", "V", "~'1.2'.dup@1"},
		{"ruby", "V = '1.2'.freeze.upcase\n", "V", "~'1.2'.freeze.upcase@1"},
		{"ruby", "V = [1].freeze\n", "V", "~[1].freeze@1"},
		{"ruby", "V = :a.freeze\n", "V", "~:a.freeze@1"},
		{"ruby", "V = \"#{x}\".freeze\n", "V", "~\"#{x}\".freeze@1"},
		{"python", "V = '1.2'.freeze\n", "V", "~'1.2'.freeze@1"},
	})
	if got := read(t, "ruby", "VERSION = '0.5.0'.freeze\n", "VERSION"); got != "0.5.0@1" {
		t.Errorf("literal reads %s", got)
	}
}

// A change is read once for each scope that sets its name, not once for
// each class it is nested in (review 1, F6): m changes of X inside 999
// classes that each set X take at most four times as long as inside one,
// and a tenth of a second. 2.8 MB of such changes took 7.9 s nested 999
// deep and 0.47 s one deep before. Planted break: tainted not stopping at
// the first change that sets no key. It is slow under make test's race and
// coverage, so make slow runs it; TestChangesInNestedClasses checks what a
// nested change reads as on every run.
func TestNestedChangesReadOnce(t *testing.T) {
	slowOnly(t)
	nested := func(depth, m int) string {
		var b strings.Builder
		for i := range depth {
			b.WriteString("class A" + strconv.Itoa(i) + " {\nstatic X = 1\n")
		}
		b.WriteString(strings.Repeat("static { X += 1 }\n", m))
		return b.String() + strings.Repeat("}\n", depth)
	}
	l, _ := core.LanguageNamed("javascript")
	timed := func(src string) time.Duration {
		fastest := time.Duration(math.MaxInt64)
		for range 3 {
			start := time.Now()
			if _, err := ParseMembers("f", l, []byte(src)); err != nil {
				t.Fatal(err)
			}
			fastest = min(fastest, time.Since(start))
		}
		return fastest
	}
	const m = 20000
	flat, deep := timed(nested(1, m)), timed(nested(core.MaxDepth-1, m))
	t.Logf("%d changes: %v one class deep, %v 999 deep", m, flat, deep)
	if limit := 4*flat + 100*time.Millisecond; deep > limit {
		t.Errorf("%d changes 999 classes deep took %v, over %v", m, deep, limit)
	}
}

// A change inside the innermost of nested classes that each set X changes
// X, and a change before a class is no change to its member. Planted
// break: tainted reading changes from the start of the file.
func TestChangesInNestedClasses(t *testing.T) {
	nested := "class A0 {\nstatic X = 1\nclass A1 {\nstatic X = 1\nclass A2 {\nstatic X = 1\nstatic { X += 1 }\nstatic { X += 1 }\n}\n}\n}\n"
	if got := member(t, "javascript", nested, "A0.A1.A2.X"); got != "~1@6" {
		t.Errorf("X changed in the innermost class: %s", got)
	}
	if got := member(t, "javascript", "X += 1\nclass A {\n  static X = 1\n}\n", "A.X"); got != "1@3" {
		t.Errorf("X changed before the class: %s", got)
	}
}

// A call through this or self changes the member it is called on only when
// the method is one of the row's Methods: self.ITEMS.append(1) does,
// self.HOST.rstrip('/') and this.name.equals(x) read, and self.f() calls a
// method of the class. A call on any other name still changes it (delta
// review 1, D2). Planted break: call changing a self chain's member for
// any method.
func TestCallsThroughSelf(t *testing.T) {
	members(t, []struct{ lang, src, name, want string }{
		{"python", "class C:\n    HOST = 'h/'\n    def f(self):\n        return self.HOST.rstrip('/')\n", "C.HOST", "h/@2"},
		{"python", "class C:\n    ITEMS = ['a']\n    def f(self):\n        self.ITEMS.append('b')\n", "C.ITEMS", "~['a']@2"},
		{"python", "class C:\n    D = {}\n    def f(self):\n        self.D.update(a=1)\n", "C.D", "~{}@2"},
		{"python", "class C:\n    X = 1\n    def f(self):\n        return self.g()\n", "C.X", "1@2"},
		{"python", "L = ['a']\nL.copy()\n", "L", "~['a']@1"},
		{"javascript", "class C {\n  static NAME = 'n'\n  f(x) { return this.NAME.startsWith(x) }\n}\n", "C.NAME", "n@2"},
		{"javascript", "class C {\n  static LIST = []\n  f(x) { this.LIST.push(x) }\n}\n", "C.LIST", "~[]@2"},
		{"typescript", "class C {\n  readonly name = 'n'\n  f(x: string) { return this.name.toUpperCase() }\n}\n", "C.name", "!n@2"},
		{"typescript", "class C {\n  readonly m = new Map()\n  f() { this.m.set('a', 1) }\n}\n", "C.m", "~new Map()@2"},
		{"java", "class C {\n  static final String NAME = \"n\";\n  boolean f(String x) { return this.NAME.equals(x); }\n}\n", "C.NAME", "!n@2"},
		{"java", "class C {\n  final java.util.List<String> L = new java.util.ArrayList<>();\n  void f() { this.L.add(\"x\"); }\n}\n", "C.L", "~new java.util.ArrayList<>()@2"},
		{"ruby", "class C\n  LIST = ['a']\n  def f\n    self.LIST.push 'b'\n  end\nend\n", "C.LIST", "~['a']@2"},
		{"ruby", "class C\n  NAME = 'n'\n  def f\n    self.NAME.upcase\n  end\nend\n", "C.NAME", "n@2"},
	})
}
