//go:build !mutation

package cli

// mutationRun reports whether the tests run under Gremlins, which builds
// them with the mutation tag (.gremlins.yaml) and reruns the package once
// for every mutant, in a copy of the module that holds no .git.
const mutationRun = false
