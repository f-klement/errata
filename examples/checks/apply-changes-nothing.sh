#!/bin/sh
# Claim: applying the manifests changes nothing in the cluster. kubectl diff exits 1 on a change.
exec kubectl diff -f "$ERRATA_ROOT/deploy/manifests.yaml"
