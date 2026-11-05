#!/bin/sh
# Claim: the public host answers on HTTPS.
exec curl -fsS -o /dev/null https://larder.example.com/health
