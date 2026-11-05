#!/bin/sh
# Claim: a server side dry run under the deploy's field manager leaves no field to another manager.
out=$(kubectl apply --server-side --dry-run=server --field-manager=larder-deploy \
  -f "$ERRATA_ROOT/deploy/manifests.yaml" --show-managed-fields -o yaml) || exit 2
printf '%s\n' "$out" | grep -q 'manager: kubectl-edit' && { echo "kubectl-edit keeps a field"; exit 1; }
exit 0
