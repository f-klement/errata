"""Prints, for each file named, one line of JSON: every section, key and
value Python's configparser reads from it, in order, or the refusal.

It reads with the defaults but for what the plain dialect documents it
reads differently: keys keep their case, a repeated key or section is not
refused, the keys before the first header are in the section with no name,
DEFAULT is a section like any other, and nothing is interpolated.
TestRegenerateOracleCorpora runs it; see ini_oracle_test.go.
"""
import configparser
import json
import sys


def read(path):
    got = []

    class Recorder(dict):
        def __setitem__(self, key, value):
            if isinstance(value, list):
                got.append((self, key, value))
            super().__setitem__(key, value)

    parser = configparser.RawConfigParser(
        dict_type=Recorder, strict=False, interpolation=None,
        default_section="\x00", allow_unnamed_section=True)
    parser.optionxform = str
    names = {}
    try:
        with open(path, encoding="utf-8") as f:
            parser.read_file(f)
    except (configparser.Error, UnicodeDecodeError) as e:
        return {"refused": type(e).__name__}
    for name, section in parser._sections.items():
        names[id(section)] = "" if name is configparser.UNNAMED_SECTION else name
    return {"entries": [[names[id(s)], k, "\n".join(v).rstrip()] for s, k, v in got]}


for path in sys.argv[1:]:
    print(json.dumps(read(path)))
