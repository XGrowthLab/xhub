# Deployment backup inventory and rotation

A PostgreSQL archive captures a data recovery point. A deployment record captures the Git commit, image digests, Compose and proxy configuration, and the version references needed to recover secrets. Link each release to its applicable data recovery point.

## Limit backup growth per release

For application releases with verified database compatibility in both directions, retain the independent database backup schedule. Record both image versions, the rollback configuration and the most recent restore-tested recovery point. Rehearse starting the previous application against the database after the upgrade and preserving intervening writes. Retain the configuration versions and secure access needed for that rollback.

Create a fresh recovery point for schema changes, backfills, deletion, a database version upgrade or uncertain compatibility. Verify a complete archive or a point-in-time recovery target with continuous logs. Restoring an older recovery point can discard writes between the point and deployment. Reconcile the durable billing spool separately.

## Read-only local inventory

```sh
python3 scripts/deployment_backup_plan.py --root "$BACKUP_ROOT"
python3 scripts/deployment_backup_plan.py --root "$BACKUP_ROOT" --inspect-limit 8
```

The tool inventories known archive files in deployment subdirectories. The first command reads file metadata. The second checks up to eight archive tables of contents using a PostgreSQL client that supports the archive format and compression. An archive with table-data entries for users, keys, organizations, usage and history is classified as a full-backup candidate. Schema-only and account-only snapshots remain separate. Verify recoverability with an actual restore.

Reports omit archive contents, connection strings and offsite references. Total allocation for recognized archives is deduplicated by device and inode. Other files, unrecognized layouts, filesystem compression and snapshots can affect disk reconciliation. The tool orders archives by modification time. Compare this ordering with actual recovery points before scheduling rotation.

## Restore evidence and rotation candidates

Store the archive in an independent failure domain and verify its SHA-256. Restore that copy completely with `pg_restore --exit-on-error` into an isolated database. Verify the schema, key row counts and amounts, ownership and history reads. Record the recovery point and restoration time. Keep evidence written by the verifier in a separate read-only directory. The filename is `<deployment-directory>.<archive-file>.json`.

| Fields | Source |
| --- | --- |
| `snapshot`, `archive`, `bytes` | Deployment directory, archive filename and byte length |
| `sha256`, `offsite_sha256`, `restored_sha256` | Independently measured SHA-256 of the local archive, the offsite copy read back and the archive used for the full restore |
| `restore_scope`, `restore_validated` | Record `full_database` and `true` after a complete restore and validation |
| `offsite_reference`, `restored_at` | The verified retained offsite object reference and timezone-aware completion time |

```sh
python3 scripts/deployment_backup_plan.py --root "$BACKUP_ROOT" --inspect-limit 8 \
  --proof-dir "$BACKUP_PROOF_DIR" --verify-limit 4 --keep 3
```

The command rehashes up to four local archives with evidence, which can require substantial sequential I/O. Use a quiet period or a snapshot. Verify the offsite copy and the restore independently. The command reads the resulting evidence and verifies that it matches local bytes. It protects the three most recent full candidates and any deployment pinned with `--pin`, and requires a verified retained local checkpoint. An older archive with matching replication and restore evidence becomes a manual review candidate. Check live references, the continued availability of offsite copies, and organizational retention requirements before scheduling rotation. The command only reports candidates.
