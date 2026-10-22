"""Prints, for each file named, one line of JSON: every section, key and
value tox 4 reads from it as tox.ini, in order, each value as
IniLoader.process_raw makes it outside an environment, or the refusal.
TestRegenerateOracleCorpora runs it; see ini_oracle_test.go.
"""
import json
import sys
from pathlib import Path

from tox.config.loader.ini import IniLoader
from tox.config.source.ini import IniSource


def read(path):
    try:
        parser = IniSource(Path(path))._parser
    except Exception as e:  # tox raises ValueError, and UnicodeDecodeError
        return {"refused": type(e).__name__}
    entries = []
    for name, section in parser._sections.items():
        for key, value in section.items():
            entries.append([name, key, IniLoader.process_raw(None, None, value)])
    return {"entries": entries}


for path in sys.argv[1:]:
    print(json.dumps(read(path)))
