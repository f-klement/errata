package core

// What the external tests of package core_test use and only they need. They
// are external because they read files through the readers, which import
// this package.

const Dockerfile = dockerfile
const MaxFileName = maxFileName
const MaxLinks = maxLinks
const MaxQuote = maxQuote

type TreeExtractor = treeExtractor

var CheckFilterSelectsLess = checkFilterSelectsLess
var CheckQuerySplits = checkQuerySplits
var CheckTyped = checkTyped
var ParsePath = parsePath

// Select is what p selects in roots, against a filter budget of its own.
func Select(p Path, roots []*Node) ([]*Node, error) {
	var m meter
	return p.eval(&m, roots)
}
