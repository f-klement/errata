#!/bin/sh
# Claim: the running web Deployment runs the tag the chart names.
want=$(sed -n 's/^  tag: //p' "$ERRATA_ROOT/deploy/values.yaml")
got=$(kubectl get deployment web -o jsonpath='{.spec.template.spec.containers[0].image}')
[ "${got##*:}" = "$want" ] && exit 0
echo "the cluster runs $got, the chart names $want"
exit 1
