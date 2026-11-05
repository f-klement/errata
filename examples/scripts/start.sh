#!/bin/sh
# Starts the API on the port the chart exposes.
API_PORT=8080
export API_PORT
exec python3 src/server.py
