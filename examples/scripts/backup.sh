#!/bin/bash
# Copies the database dump to the backup directory. An operator may point it elsewhere for one
# run with BACKUP_TARGET.
BACKUP_DIR=/var/backups/larder
if [[ -n "${BACKUP_TARGET:-}" ]]; then
  BACKUP_DIR="${BACKUP_TARGET}"
fi
# Never kubectl apply from here: deploys go through the pipeline.
cp /tmp/larder.dump "$BACKUP_DIR/"
