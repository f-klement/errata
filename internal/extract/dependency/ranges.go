package dependency

import (
	"slices"
	"strings"
)

// The readers of Cargo.toml, composer.json and the Gemfile hand back the
// range their tool reads, written in the syntax errata's semver reads, which
// is npm's where the ecosystems differ (internal/semver, ParseRange). Each
// rewrites only what its tool reads otherwise, so an agree or a
// semver_satisfies over a manifest and its lockfile answers as the package
// manager would.

// cargoRange is the range Cargo reads in r: a comparator with no operator,
// such as 1.38 in tokio = "1.38", takes the ^ Cargo implies, so 1.40.0
// satisfies it. A wildcard, such as 1.*, is left as Cargo reads it.
func cargoRange(r string) string {
	parts := strings.Split(r, ",")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if bare(p) {
			p = "^" + p
		}
		parts[i] = p
	}
	return strings.Join(parts, ", ")
}

// composerRange is the range Composer reads in r. A bare version, such as
// 1.2, is that version exactly, and is written ==1.2; so is =1.2. A tilde
// of two numbers, ~1.2, is >=1.2 <2.0 in Composer, where npm reads it as
// <1.3, and is written ~>1.2, which reads so; a tilde of three numbers
// reads the same in both. A single | between alternatives is ||. A
// stability flag after @, as in ^3.0@dev, says which releases Composer may
// pick, not which versions, and is cut; a flag alone, @dev, is *. A hyphen range, 1.0 - 2.0, is left
// as it is.
func composerRange(r string) string {
	var alts []string
	for alt := range strings.SplitSeq(r, "|") {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue // the second | of a ||
		}
		alts = append(alts, composerAlternative(alt))
	}
	return strings.Join(alts, " || ")
}

// composerAlternative is one alternative of a Composer range, rewritten.
func composerAlternative(alt string) string {
	fields := strings.Fields(strings.ReplaceAll(alt, ",", " "))
	if len(fields) == 3 && fields[1] == "-" {
		return alt
	}
	var tokens []string
	for _, f := range fields {
		f, _, _ = strings.Cut(f, "@")
		if f == "" {
			// A stability flag alone, @dev, is any version at that
			// stability.
			f = "*"
		}
		if n := len(tokens); n > 0 && slices.Contains(operators, tokens[n-1]) {
			tokens[n-1] += f
			continue
		}
		tokens = append(tokens, f)
	}
	for i, t := range tokens {
		switch {
		case bare(t):
			tokens[i] = "==" + t
		case strings.HasPrefix(t, "=") && !strings.HasPrefix(t, "==") && bare(t[1:]):
			tokens[i] = "=" + t
		case strings.HasPrefix(t, "~") && !strings.HasPrefix(t, "~>") && bare(t[1:]) && strings.Count(t, ".") == 1:
			tokens[i] = "~>" + t[1:]
		}
	}
	return strings.Join(tokens, " ")
}

// operators are the operators a range may write apart from its version, as
// in >= 1.0, which joins the version after it.
var operators = []string{"~>", "==", "!=", ">=", "<=", "^", "~", ">", "<", "="}

// exactRange is the range RubyGems reads in r, one requirement of a gem
// statement, and the range Poetry reads in a dependency's string: a bare
// version, such as "7.1", is that version exactly, and so is "= 7.1"; both
// are written ==7.1, since a bare 7.1 in npm's syntax is every 7.1 release.
func exactRange(r string) string {
	t := strings.TrimSpace(r)
	if rest, ok := strings.CutPrefix(t, "="); ok && !strings.HasPrefix(rest, "=") {
		t = strings.TrimSpace(rest)
	}
	if bare(t) {
		return "==" + t
	}
	return r
}

// bare reports whether t is a version with no operator and no wildcard: a
// digit, or a v and a digit, first, and no * or x in place of a number.
func bare(t string) bool {
	s := strings.TrimPrefix(t, "v")
	if s == "" || !isDigit(s[0]) {
		return false
	}
	for p := range strings.SplitSeq(s, ".") {
		if p == "*" || p == "x" || p == "X" {
			return false
		}
	}
	return true
}

// composerBranch reports whether a Composer version names a branch, as
// dev-main or 2.x-dev do: a branch moves, so it names no version in the
// file.
func composerBranch(v string) bool {
	for f := range strings.FieldsFuncSeq(v, func(r rune) bool { return r == ' ' || r == ',' || r == '|' }) {
		f, _, _ = strings.Cut(f, "@")
		if strings.HasPrefix(f, "dev-") || strings.HasSuffix(f, "-dev") {
			return true
		}
	}
	return false
}
