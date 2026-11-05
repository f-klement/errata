package htmlparse

// A template's content is no part of the page: the scanner gives nothing
// from a <template> to its </template>. It still follows the content's
// elements, in a nest of their own, since x/net's tree builder parses the
// content as it parses a page, with the template on its stack: an svg
// there is foreign content, whose title or style the tokenizer must not
// read raw, or the rest of the file would read as that element's text.
// The nest is inert, as no pre or heading in it is read, and a scope or a
// special element search there stops at the template, so no rule of the
// content reaches the page's elements around it.

// active is the nest the scanner follows: the template content's while a
// template is open, else the page's.
func (s *Scanner) active() *nest {
	if s.tmpl != nil {
		return s.tmpl
	}
	return &s.nest
}

// templateTag follows a tag read as HTML's inside a template's content,
// and reports false: the scanner gives none. A </template> closes the
// nearest template open in the content, or the template itself.
func (s *Scanner) templateTag(selfClosing bool) bool {
	t := s.tmpl
	switch {
	case s.closing && s.name == "template":
		if i := t.last(t.posOf("template")); i >= 0 {
			t.popThrough(i)
		} else {
			s.tmpl = nil
		}
	case s.closing:
		t.end(s.name) // inert: no refusal
	default:
		name, opens := s.name, true
		if name == "svg" || name == "math" {
			name, opens = foreignKey(name, name), !selfClosing
		}
		if err := t.start(name, opens && !void[name]); err != "" {
			s.err = &NestError{At: s.start, msg: err}
		}
	}
	return false
}
