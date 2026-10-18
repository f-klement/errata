//go:build !mutation

package report

// mutationRun reports whether the tests run under Gremlins, which builds
// them with the mutation tag (.gremlins.yaml) and reruns the package once
// for every mutant.
const mutationRun = false
