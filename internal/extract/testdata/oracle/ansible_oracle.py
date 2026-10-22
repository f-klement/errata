"""Prints, for each inventory file named, one line of JSON: what
ansible-core's ini inventory plugin reads from it, or the refusal. A host's
and a group's variables are the last value of each key: a str as the hex of
its bytes (ansible reads a byte that is not UTF-8 as a surrogate, which
surrogateescape gives back), and any other value as {"repr": repr(v)}.
TestRegenerateOracleCorpora runs it; see ini_oracle_test.go.
"""
import json
import sys

from ansible.inventory.data import InventoryData
from ansible.parsing.dataloader import DataLoader
from ansible.plugins.loader import inventory_loader

MAGIC = {"inventory_file", "inventory_dir"}


def text(v):
    if isinstance(v, str):
        try:
            return str(v).encode("utf-8", "surrogateescape").hex()
        except UnicodeEncodeError:  # a surrogate from an escape, which no bytes hold
            return {"unencodable": repr(v)}
    return {"repr": repr(v)}


def read(path):
    inventory = InventoryData()
    try:
        inventory_loader.get("ini").parse(inventory, DataLoader(), path, cache=False)
    except Exception as e:
        cause = e.__cause__ or e
        return {"refused": str(cause)}
    return {
        "hosts": {g.name: [h.name for h in g.hosts] for g in inventory.groups.values() if g.name != "ungrouped"},
        "children": {g.name: [c.name for c in g.child_groups] for g in inventory.groups.values()},
        "group_vars": {g.name: {k: text(v) for k, v in g.vars.items()} for g in inventory.groups.values()},
        "host_vars": {h.name: {k: text(v) for k, v in h.vars.items() if k not in MAGIC} for h in inventory.hosts.values()},
    }


for path in sys.argv[1:]:
    print(json.dumps(read(path)))
