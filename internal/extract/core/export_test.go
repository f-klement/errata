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
var CheckTyped = checkTyped
var ParsePath = parsePath
