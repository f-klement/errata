package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// copyFloor is how long a check's last run must have taken for a copy to
// pay for it: a copy costs about 0.15 s to make and remove, so a check of
// a second or more gains what its copy costs five times over by running
// beside another, and a shorter one gains too little to be sure of.
const copyFloor = time.Second

// durationsFile is the record of how long each check last ran, in the
// repository's directory of the user's cache (treecopy.Home).
const durationsFile = "durations.json"

// durations is how long each program and generated check last ran, by its
// name in the report (check.Result.Name), in milliseconds.
type durations map[string]int64

// readDurations is the record in home. A record that is missing, cannot be
// read or does not decode is no record: each check then copies.
func readDurations(home string) durations {
	b, err := os.ReadFile(filepath.Join(home, durationsFile))
	if err != nil {
		return nil
	}
	var d durations
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	return d
}

// pays reports whether a copy pays for the job named name: it has no
// record, or its last run took copyFloor or more.
func (d durations) pays(name string) bool {
	ms, ok := d[name]
	return !ok || time.Duration(ms)*time.Millisecond >= copyFloor
}

// writeDurations records in home how long each job that may copy ran, the
// ones runsProgram names, replacing the record. It writes a file beside
// the record and renames it over it, so a run that reads the record at
// the same time reads the old one or the new one whole, and of two runs
// that write it the last one stands.
func writeDurations(home string, jobs []job, results []check.Result) error {
	d := durations{}
	for i, j := range jobs {
		if j.runs() && runsProgram(j.c) {
			d[results[i].Name()] = results[i].Duration.Milliseconds()
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(home, durationsFile+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err = errors.Join(err, f.Close()); err == nil {
		err = os.Rename(f.Name(), filepath.Join(home, durationsFile))
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
