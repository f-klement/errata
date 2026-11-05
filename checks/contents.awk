# contents.awk: the program of checks/contents.sh, which says what it checks. Its input is one
# path a line; checks/markdown.awk comes first.
{
    f = $0
    anchors(f)
    c = 0
    for (i = 1; i <= heads[f]; i++) if (hlevel[f, i] == 2 && htext[f, i] == "Contents") { c = i; break }
    if (!c) { print f ": no heading ## Contents"; nocontents = 1; next }
    # The entries: each line of the Contents section, blank lines aside.
    m = 0; inside = 0
    fence_reset()
    while ((getline line < f) > 0) {
        if (fenced(line)) { if (inside) { print f ": Contents holds a fenced block"; bad = 1 }; continue }
        lv = heading(line)
        if (lv) { if (inside) break; if (lv == 2 && heading_text(line) == "Contents") inside = 1; continue }
        if (!inside || line ~ /^[ \t]*$/) continue
        if (line !~ /^ *[-*] \[.*\]\(#[^)]*\)$/) { print f ": Contents holds a line that is no entry: " line; bad = 1; continue }
        t = line; sub(/^ */, "", t); ind[++m] = length(line) - length(t)
        sub(/^[-*] \[/, "", t); a = t
        sub(/\]\(#[^)]*\)$/, "", t); etext[m] = t
        match(a, /\]\(#[^)]*\)$/); eanchor[m] = substr(a, RSTART + 3, RLENGTH - 4)
    }
    close(f)
    fence_reset()
    # What they must be: every heading of level 2 or 3 after Contents, in order, its entry
    # indented two spaces for level 3, with the heading's text and its anchor.
    k = 0
    for (i = c + 1; i <= heads[f]; i++) {
        if (hlevel[f, i] > 3) continue
        k++
        want = (hlevel[f, i] == 3 ? "  " : "") "- [" htext[f, i] "](#" hanchor[f, i] ")"
        got = k <= m ? substr("          ", 1, ind[k]) "- [" etext[k] "](#" eanchor[k] ")" : "nothing"
        if (got != want) { print f ": Contents entry " k " is " got ", where the heading there wants " want; bad = 1 }
    }
    for (k = k + 1; k <= m; k++) { print f ": Contents entry " k " names no heading: [" etext[k] "](#" eanchor[k] ")"; bad = 1 }
}
# The verdict is the last line, so that awk's own failure, whatever status it exits with, is
# never read as one.
END { print "verdict " (nocontents ? 2 : (bad ? 1 : 0)) }
