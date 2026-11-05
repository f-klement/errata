# relative-links.awk: the program of checks/relative-links.sh, which says what it checks. Its
# input is one line per file, md or go, a space and the path; checks/markdown.awk comes first.
# resolve(dir, target): the path target names from dir, with . and .. folded.
function resolve(dir, target,   p, parts, n, i, out, m) {
    p = (dir == "." || dir == "") ? target : dir "/" target
    n = split(p, parts, "/")
    m = 0
    for (i = 1; i <= n; i++) {
        if (parts[i] == "." || parts[i] == "") continue
        if (parts[i] == ".." && m > 0 && out[m] != "..") { m--; continue }
        out[++m] = parts[i]
    }
    p = ""
    for (i = 1; i <= m; i++) p = p (i > 1 ? "/" : "") out[i]
    return p == "" ? "." : p
}
function exists(p,   e) {
    e = p; gsub(q, q "\\" q q, e)
    return system("[ -e " q e q " ]") == 0
}
# check(f, dir, target): prints what is wrong with the link target that f
# writes, read from dir, if anything.
# Each message names the target as f writes it, a mirror link's URL whole.
function check(f, dir, target,   path, frag, to, at, written) {
    sub(/^</, "", target); sub(/>$/, "", target)
    written = target
    # A link to a file of the public mirror, as DOCKERHUB.md writes them, names a file of
    # this tree, read from the root.
    if (sub(/^https:\/\/github\.com\/f-klement\/errata\/blob\/main\//, "", target)) dir = "."
    if (target == "" || target ~ /^[a-z][a-z0-9+.-]*:/) return
    path = target; frag = ""
    if (index(target, "#")) { at = index(target, "#"); path = substr(target, 1, at - 1); frag = substr(target, at + 1) }
    to = path == "" ? f : resolve(dir, path)
    # The mirror has no parent, and a checkout's parent is no file of the repository, so a path
    # that climbs above the root resolves on no forge, whatever the disk holds there.
    if (to ~ /^\.\.(\/|$)/) { print f ": " written ": the path climbs out of the repository"; return }
    if (path != "" && !exists(to)) { print f ": " written; return }
    if (frag == "" || to !~ /\.md$/) return
    anchors(to)
    if (!((to, frag) in has)) {
        print f ": " written ": no heading of " to " has the anchor " frag (unsure_in[to] ? "; some of its headings hold a character outside ASCII, emphasis, HTML or an entity, whose anchors this check cannot write as GitHub does" : "")
    } else if (has[to, frag] == 2) {
        print f ": " written ": the heading of " to " with the anchor " frag " holds a character outside ASCII, emphasis, HTML or an entity, whose anchor this check cannot write as GitHub does; write the heading in ASCII, without emphasis, HTML or entities"
    }
}
{
    kind = $1; f = substr($0, 4); sub(/^\.\//, "", f)
    dir = f; if (!sub(/\/[^\/]*$/, "", dir)) dir = "."
    # The links are read first and checked once the file is closed, since
    # anchors() reads a file of its own, this one too, from its first line.
    n = 0
    fence_reset()
    while ((getline line < f) > 0) {
        if (kind == "go") {
            while (match(line, /docs\/[A-Za-z0-9_.\/-]*\.md#[A-Za-z0-9_-]+/)) {
                link[++n] = substr(line, RSTART, RLENGTH); from[n] = "."
                line = substr(line, RSTART + RLENGTH)
            }
            continue
        }
        if (fenced(line)) continue
        gsub(/`[^`]*`/, "", line)
        if (line ~ /^ ? ? ?\[[^]^][^]]*\]:/) {
            t = line; sub(/^[^]]*\]:[ \t]*/, "", t); sub(/[ \t].*$/, "", t)
            link[++n] = t; from[n] = dir
        }
        while (match(line, /\]\([^)]*\)/)) {
            t = substr(line, RSTART + 2, RLENGTH - 3); sub(/^[ \t]+/, "", t)
            if (substr(t, 1, 1) == "<") { sub(/>.*$/, ">", t) } else { sub(/[ \t].*$/, "", t) }
            link[++n] = t; from[n] = dir
            line = substr(line, RSTART + RLENGTH)
        }
    }
    close(f)
    fence_reset()
    for (i = 1; i <= n; i++) check(f, from[i], link[i])
}
