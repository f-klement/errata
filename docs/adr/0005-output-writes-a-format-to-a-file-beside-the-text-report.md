# --output writes a format to a file beside the text report

A CI job wants two things from one `errata check`: a JUnit or SARIF file for the pipeline to read,
and the errata in its log for a person to read. `--format` picks one format for stdout, so a job
that redirected JUnit into a file printed nothing a person could read, and the k3s repository kept
a 20 line JUnit reader only to print the failures. `errata check` takes a repeatable
`--output FORMAT=PATH` (short `-o`), which writes the report in FORMAT to PATH, while `--format`
keeps choosing what goes to stdout:

```text
errata check --output junit=errata.xml --output sarif=errata.sarif
```

The exit code, and the content of each format, are what they are without the flag.

## The rules

- FORMAT is any name `--format` takes, `text` included: a job may keep the text report as an
  artifact. PATH is taken from the working directory, as a shell redirect takes it, not from
  `--root`. The value splits at its first `=`, so a path may hold one; the flag is a string
  array, so a comma in a path is not a separator.
- A value without `=`, with an empty FORMAT or PATH, with a FORMAT errata has no reporter for, or
  two values that name the same file after `filepath.Abs` (`errata.xml` and `./errata.xml`) are a
  usage error: exit 2 and the reason on stderr, before the config is loaded, so nothing runs. One
  format may go to two files. errata does not follow symbolic links to compare paths. An output
  whose absolute path is the config file's, the `--config` file or `errata.yaml` in the root, is
  exit 2 once the config has loaded and before any check runs: a passing run would otherwise
  replace the config with its report and exit 0, where `> errata.yaml` emptied the config first
  and the run failed.
- `--format` and `--output` do not interact. `--format junit --output sarif=errata.sarif` prints
  JUnit on stdout and writes SARIF to the file.
- Each file is written whole or not at all. Before any check runs, errata creates a temporary
  file for each output in the directory of its PATH, named `.<name>.<random>.tmp`, with
  `O_CREATE|O_EXCL` and mode 0666, so the umask sets the mode as it does for a redirect into a
  new file. A directory that does not exist or cannot be written, or a PATH that is a directory,
  is exit 2 before any check runs, and errata removes the temporary files it had created. After
  the run, errata writes each report to its temporary file and renames it over PATH. A regular
  file that is there already is replaced, and the report takes its mode, as a redirect into it
  would keep it, so a report never widens who may read the file; the owner is not kept. A file
  that cannot be replaced is left as it was.
- A PATH that is a symbolic link, to a file or to a directory, is replaced by the report: errata
  does not follow the link, and does not write through it.
- Every file is written whatever the run found: a pass, an erratum, an unknown or a skip under
  `--fail-on-skip`.
- A file that cannot be written after the run, or a stdout that cannot be written, is exit 2, with
  each failure on the stderr line that starts `errata: `. errata still writes the text report to
  stdout and every other output, and removes the temporary file of the one that failed. Exit 2
  takes the place of exit 1 on a run with errata: the job asked for a file it does not get, and
  the report on stdout still shows the errata.

## Considered options

- **Keep the redirect and add a flag that prints the errata on stderr.** It covers one machine
  format, needs no file handling, and the k3s job would change one flag. It does not write JUnit
  and SARIF from one run, and the text report would go to stderr, where a CI log mixes it with
  the checks' own messages.
- **`--junit FILE` and `--sarif FILE`, one flag per format.** Each reads clearly, but each new
  format would add a flag, and `text` would need one too. `--output FORMAT=PATH` names the format
  with the word `--format` already takes.
- **`--format junit,sarif` with `--output-dir`.** It fixes the names of the files, and the
  pipeline's configuration names them, so errata would decide something that belongs to the job.
- **Exit 1 when only a file failed.** A job that reads exit 1 as "errata found" would look for a
  JUnit file that is not there and blame the checks. Exit 2 says that errata could not do what the
  command line asked, as for a config that does not load.
- **Write PATH in place.** A run that fails in the middle of a write would leave a part of an XML
  file, which a CI system reads as a broken report or as fewer tests. The temporary file and the
  rename cost one more file in the same directory.

## Consequences

`--output` is part of the command line contract from v0.2.1. A run with outputs creates its
temporary files in the output directories before the checks run, as a shell creates the target
of `>` before the program starts; a check that lists the files of that directory sees them. A
temporary file outlives a run that is killed, such as by the CI job's timeout: it holds part of
a report or none, beside PATH, which keeps the file that was there before.
