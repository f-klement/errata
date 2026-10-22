//go:build !mutation

package extracttest

// MutationRun reports whether the tests run under Gremlins, which builds
// them with the mutation tag (.gremlins.yaml) and reruns a package once for
// every mutant.
const MutationRun = false
