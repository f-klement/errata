//go:build mutation

package main

// Every tested package links the heap watchdog under the mutation tag
// (internal/heapcap, TestEveryTestedPackageLinksTheWatchdog), this one too,
// though scripts/mutation.sh mutates only the packages under internal/.
import _ "github.com/f-klement/errata/internal/heapcap"
