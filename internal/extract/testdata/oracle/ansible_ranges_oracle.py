"""Prints, for each file named, one line of JSON: the hosts ansible-core's
ini inventory plugin expands the host pattern the file holds to
(_expand_hostpattern), or the refusal. The generated patterns expand to a
few hundred hosts at most. TestRegenerateOracleCorpora runs it under a
memory cap; see ini_oracle_test.go.
"""
import json
import sys

from ansible.plugins.inventory.ini import InventoryModule

module = InventoryModule()
for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as f:
        pattern = f.read()
    try:
        hosts, _ = module._expand_hostpattern(pattern)
        print(json.dumps({"expanded": [str(h) for h in hosts]}))
    except Exception as e:
        print(json.dumps({"refused": str(e)[:200]}))
