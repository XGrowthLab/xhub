# Conversation history storage

The unified Responses endpoint stores conversation history for continuation. Full snapshots can repeat earlier messages across turns. The compact format stores lossless message blocks within the existing caller, public model and endpoint scope, and stores an ordered block index for each response. Larger blocks are compressed and verified by length and digest. Authentication, budgets, native bypass routing and log retention are unchanged.

New tables `conversation_history_blocks` and `conversation_history_manifests` are created on startup. Both formats are readable. New writes use the old format by default; set `XHUB_COMPACT_HISTORY=1` in the process environment and restart to enable compact writes. Disabling it affects future writes only. A rollback must still use a binary capable of reading compact records. Block and index writes are transactional; corrupted or missing blocks fail continuation, and a failed history write cannot publish a successful Responses terminal event.

For a canary, set `XHUB_COMPACT_HISTORY=canary` and put the verified XHub user IDs in the comma-separated `XHUB_COMPACT_HISTORY_USER_IDS` variable. Console sessions and personal API keys match the authenticated user ID. A missing or empty list leaves new writes in the old format. Upgrade every instance to a binary that reads both formats before the canary. Check JSON and SSE continuation, database write volume, and read latency for a canary account and a control account before expanding. To roll back future writes, set `XHUB_COMPACT_HISTORY=0` and restart. Existing compact histories remain readable. The canary needs no migration or deletion of existing history.

Use the same mode and list on gateway instances serving the same traffic. Mode values are case-sensitive after trimming surrounding whitespace. Mode `1` enables compact writes for all callers and ignores the list. An unknown value uses legacy writes and generates one diagnostic when a matching endpoint is first called. Removing accounts from the list affects subsequent requests. If a response key is overwritten in the legacy format, the transaction removes that key's compact manifest. Its content blocks remain stored. There is currently no automatic collection of unreferenced content blocks.

## Migrating legacy records

A legacy key is a one-way digest, so its original caller scope cannot safely be reconstructed across different responses. The migration assigns each old record a separate scope, keeps the old copy, and verifies each reconstructed history in the transaction. It does not deduplicate across old records, remove legacy rows, or collect unreferenced blocks; storage can temporarily grow. Newly written responses can share blocks within their authenticated scope.

Confirm that every running gateway understands the compact format and that a complete backup has been restored and verified separately. Run one batch with:

```sh
XHUB_DATABASE_URL="$YOUR_DATABASE_URL" go run ./cmd/history-compact --execute --confirm-no-legacy-writers --limit=50
```

Resume with `--after` using the printed `next` value. Starting again from an empty cursor skips records already migrated. The confirmation flag is an operator assertion, not an automatic check of other instances. Running the migration while an older writer is active can leave its copy stale.

Before release, test JSON and streaming continuation, tool calls, identity isolation, failures, concurrent writes, migration reruns and browser flows with an isolated PostgreSQL database and local provider fixture. Measure storage and latency against representative data. Before removing any old copies, validate references, backup restoration and available disk space; ordinary PostgreSQL vacuum usually makes space reusable rather than immediately returning it to the filesystem.

## Protocol storage reports and legacy compression estimates

Run the commands against an existing schema using a read-only database account. Set `XHUB_DATABASE_URL` in the environment. Assign a unique, stable `--deployment` label to each installation and take two snapshots from the same schema.

```sh
go run ./cmd/storage-report snapshot --deployment=primary --days=7 > first.json
go run ./cmd/storage-report snapshot --deployment=primary --days=7 --baseline=first.json > second.json
```

`routes` groups usage by UTC request day, inbound and outbound protocol, explicit bypass and streaming. It counts calls and attributes the stored bytes of diagnostics, embedded bodies and separately captured documents. Empty fields count as zero. An unlinked capture uses its creation day and a null streaming flag. The window starts at midnight on the first day and ends at the observation time. Legacy history has creation dates and categories. Previously hashed routing keys cannot reliably identify individual protocols. Blocks and manifests have no creation timestamps and appear under undated.

`physical_bytes` includes the database, main tables, TOAST including its own indexes, and main-table indexes. A role with access to the WAL directory also sees current cluster-wide WAL files. A second snapshot records signed changes under `physical_delta_bytes`. Interpret these separately from the present sizes of fields attributed to their creation day. On the host, pass `--backups-dir`, `--container-logs-dir`, `--redis-dir`, `--billing-spool-dir` or `--images-dir` to measure allocated blocks on the same filesystem. Compare identical directories on the same host. Only category names appear in the report.

```sh
go run ./cmd/storage-report plan --before=2026-11-19T00:00:00Z \
  --rows=100 --batch-bytes=33554432 --record-bytes=16777216
```

The planner visits legacy history with recognized hashed keys and UTC creation times strictly before the whole-second cutoff. It reads metadata first, fetches bodies within the byte budget, tests Zstandard compression and verifies the original length and SHA-256. The default row and index allowance is 1 KiB per selected record. Adjust it with `--reserve-bytes`. Oversized or previously covered rows require metadata only. The snapshot counts unrecognized keys and unknown creation times separately. Follow `has_more` and `next_cursor` with the original cutoff. On failure, retry from the last successful cursor. Each batch has its own consistent snapshot. Recheck original content before performing a conversion.

`estimated_selected_savings_bytes` is a field-size estimate using the chosen allowance. A migration needs additional space for old copies, TOAST, WAL and rollback. Measure physical growth and filesystem reclamation on a restored database.

## Selective conversion and targeted rollback

Gateway startup adds three nullable columns to `kv` for a Zstandard frame, original length and SHA-256. The original key, authorization boundary and creation time are preserved. The reader supports legacy text, block manifests and packed legacy records. It decodes a packed record on demand and rejects unknown versions, incorrect sizes and checksum failures. Conversion runs through a separate maintenance command.

First deploy the compatible reader to every serving instance and verify ordinary, SSE and tool continuation as well as rollback. Establish and restore an independent backup covering the conversion cutoff. Use `--key-digest` for a single known history key. Start a fresh scan when moving to batches. Estimate savings with `storage-report plan` using the same bounds.

```sh
go run ./cmd/history-pack pack --execute --before=2026-11-19T00:00:00Z \
  --rows=50 --batch-bytes=16777216 --record-bytes=8388608
go run ./cmd/history-pack restore --execute --before=2026-11-19T00:00:00Z \
  --rows=50 --batch-bytes=16777216 --record-bytes=8388608
```

Each command commits one transaction. Save `next_cursor` after a successful exit and pass it via `--cursor` until `has_more=false`. Preserve the operation, database, schema, cutoff, per-record limit and selection parameters. Start rollback with an empty cursor. If a connection fails, including after an ambiguous commit, retry from the last confirmed cursor. The current row format identifies previously completed work. Start a fresh cursor for oversized records, using `--min-raw-bytes`, `--record-bytes` and `--batch-bytes`. The per-record maximum is 64 MiB.

The gateway and converter share a per-response transaction lock. Conversion rechecks the row, cutoff and manifest with a row lock and only writes valid history arrays with enough savings to cover the allowance (64 B minimum, 1 KiB default). It verifies the bytes read back from PostgreSQL before committing. An error rolls back the entire batch. Rollback only expands rows still in the packed format. A newer response takes precedence. Restore packed rows and verify a zero remaining count before downgrading to a gateway without packed readers. Use the isolated backup to validate recovery and the targeted command for in-place rollback to preserve later billing and conversations.

Monitor filesystem headroom, TOAST, WAL, replication lag and continuation latency between batches. Record reusable PostgreSQL pages and released filesystem bytes separately. Table rewrites require their own space and availability assessment.

For production conversions, encrypt the affected original records at the source and retain the ciphertext on the temporary backup host. Read back the remote archive and verify each original key digest, byte length and SHA-256 in a temporary table on the trusted source. Write the verified lowercase key digest and content SHA-256 separated by one space per line, sorted by key, to a restricted file. Pass `--backup-manifest=/restricted/batch.manifest` to `pack`. The converter checks the original under the row lock and reports missing or changed records in `not_backed_up_or_changed`. Use a fresh cursor when switching manifests. Retain the temporary ciphertext until conversion, rollback checks and observation finish.

Use `restore` for healthy frames. For a damaged frame, authenticate and decrypt the retained archive on the trusted source. Pipe the exact original UTF-8 bytes, without appending a newline, into `history-pack recover --execute --key-digest=… --created-at=… --backup-manifest=…`. Recovery requires a matching verified manifest, creation time, raw length and original digest on the current packed row. It rechecks the restored bytes in PostgreSQL. A replaced or already restored record returns `recovered=false`. Conflicting version metadata requires separate investigation.
