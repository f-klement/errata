# markdown.awk: the reading of Markdown that checks/relative-links.sh and checks/contents.sh
# share, so that both see the same fences, headings and anchors. Run with LC_ALL=C, so awk
# reads bytes and tolower changes A to Z alone, and with -v q="'", since a quote cannot sit in
# the shell's single quoted program.

# fenced(line): 1 when the line opens, closes or sits inside a fenced code block, as CommonMark
# reads one. A fence is a run of at least three backticks or tildes; a backtick fence's info
# string holds no backtick, or the line is a code span and no fence. It closes on a run of the
# same character, at least as long, with nothing after it but blanks. Call fence_reset() before
# each file.
function fenced(line,   s, c, n, rest) {
    s = line; sub(/^[ \t]*/, "", s)
    c = substr(s, 1, 1)
    if (c != "`" && c != "~") return fence_c != ""
    n = 0
    while (substr(s, n + 1, 1) == c) n++
    if (n < 3) return fence_c != ""
    rest = substr(s, n + 1)
    if (fence_c == "") {
        if (c == "`" && index(rest, "`")) return 0
        fence_c = c; fence_n = n
        return 1
    }
    if (c == fence_c && n >= fence_n && rest ~ /^[ \t]*$/) fence_c = ""
    return 1
}
function fence_reset() { fence_c = ""; fence_n = 0 }

# heading(line): the level of an ATX heading, or 0. A setext heading, text underlined with = or
# -, is not read: no file here writes one, and its anchor would be refused, never passed.
function heading(line,   s, n) {
    if (line !~ /^ ? ? ?#/) return 0
    s = line; sub(/^ */, "", s)
    n = 0
    while (substr(s, n + 1, 1) == "#") n++
    if (n > 6) return 0
    s = substr(s, n + 1)
    return (s == "" || s ~ /^[ \t]/) ? n : 0
}
# heading_text(line): the heading's text, without its marks and a closing run of #.
function heading_text(line,   s) {
    s = line
    sub(/^ *#+[ \t]*/, "", s); sub(/[ \t]+#+[ \t]*$/, "", s); sub(/^#+[ \t]*$/, "", s)
    sub(/[ \t]+$/, "", s)
    return s
}
# slug(text): the anchor GitHub gives a heading of this text: link markup reduced to the
# link's text, lower case, ASCII punctuation dropped but for - and _, each space a -. The
# number GitHub adds after a repeat is the caller's.
function slug(text,   s, t) {
    s = text
    while (match(s, /\[[^]]*\]\([^)]*\)/)) {
        t = substr(s, RSTART + 1, RLENGTH - 1); sub(/\]\(.*$/, "", t)
        s = substr(s, 1, RSTART - 1) t substr(s, RSTART + RLENGTH)
    }
    s = tolower(s)
    gsub(q, "", s)
    gsub(/[]!"#$%&()*+,.\/:;<=>?@[\\^`{|}~]/, "", s)
    gsub(/ /, "-", s)
    return s
}
# unsure(text): 1 when slug() may not write the anchor GitHub does. GitHub slugs the text it
# renders, by Unicode's tables, so the anchor is unsure when the text holds a byte outside ASCII,
# in a code span or not, which GitHub lowers or drops, or, outside a code span, an underscore
# that may be emphasis, which GitHub drops and slug() keeps, a < that may open inline HTML, whose
# tags GitHub drops, or a & that may open an entity, which GitHub renders first.
function unsure(text,   s) {
    if (text ~ /[\200-\377]/) return 1
    s = text
    gsub(/`[^`]*`/, "", s)
    if (s ~ /[<&]/) return 1
    return s ~ /(^|[^A-Za-z0-9])_|_([^A-Za-z0-9]|$)/
}
# anchors(f): reads the headings of f once. has[f, anchor] is 1 for each, or 2 for one whose
# text is unsure(); unsure_in[f] is set when any is. heads[f] counts them, and hlevel[f, i],
# htext[f, i] and hanchor[f, i] hold the i-th, in file order.
function anchors(f,   line, lv, t, a, n, i) {
    if ((f, "") in has) return
    has[f, ""] = 1
    heads[f] = 0
    fence_reset()
    while ((getline line < f) > 0) {
        if (fenced(line)) continue
        lv = heading(line)
        if (!lv) continue
        t = heading_text(line)
        a = slug(t)
        n = seen[f, a]++
        if (n) a = a "-" n
        has[f, a] = unsure(t) ? 2 : 1
        if (has[f, a] == 2) unsure_in[f] = 1
        i = ++heads[f]
        hlevel[f, i] = lv; htext[f, i] = t; hanchor[f, i] = a
    }
    close(f)
    fence_reset()
}
