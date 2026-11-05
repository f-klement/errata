#!/usr/bin/env zsh
# Starts the API with reloading, for a developer's shell.
API_PORT=8080
export API_PORT
exec python3 -m flask --app src/routes run --reload --port "$API_PORT"
