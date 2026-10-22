//go:build mutation

package ini

// Gremlins builds the tests with the mutation tag (.gremlins.yaml), and the
// watchdog stops a test binary whose heap passes the cap, so a mutant that
// makes a test allocate without limit is killed instead of the whole run.
import _ "github.com/f-klement/errata/internal/heapcap"
