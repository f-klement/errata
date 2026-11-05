#!/bin/sh
# Prints the ports .env sets, one a line, as docs/ports.txt lists them.
grep -E '^[A-Z_]+_PORT=' "$ERRATA_ROOT/.env" | sed 's/=/ /'
