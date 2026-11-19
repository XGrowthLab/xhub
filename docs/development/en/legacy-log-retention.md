# Cleanup of legacy request log bodies

Older gateway versions could store the request body, response body, and proxy request together in `request_logs`. After the introduction of personal capture settings, new bodies use `request_log_captures`, where each row carries its own expiry. Changing the global retention setting affects future captures.

## Identify the source of disk use

Use PostgreSQL's `pg_total_relation_size` to measure large relations, including their indexes and TOAST storage. Check WAL, the durable billing spool, container logs, object storage and backup retention separately. Continuation history can live in the legacy `kv` table or the newer history tables. Deleting legacy request bodies leaves continuation history intact. An ordinary vacuum makes space reusable within PostgreSQL. Preallocated files may retain their size, and backup copies follow the existing rotation policy.

## Preview and run in batches

Set `XHUB_DATABASE_URL` to a read-only connection for an existing, upgraded PostgreSQL database. Set `RETENTION_CUTOFF` to a fixed RFC 3339 timestamp at least 30 days in the past. Test against an isolated instance or a restored backup. The command runs independently of the gateway and creates no tables or indexes.

```sh
go run ./cmd/log-body-retention --before="$RETENTION_CUTOFF" --limit=100
```

Each batch returns a JSON record with scanned rows, eligible rows, committed changes, uncompressed bytes in the three legacy body columns, diagnostic rows requiring review, and a cursor. The uncompressed byte count estimates logical data volume. Pass `next` as the next `--after` value and continue until `done` is true, including pages with zero matches. A subsequent pass starting from zero can find late writes with old timestamps.

To apply the reviewed boundary, use a connection with update permissions, start from zero, keep the same cutoff, and pass `--execute`. A batch checks at most 500 usage events in a short transaction. If a connection, lock, or update fails, retry from the same cursor. For large databases, apply in stages and observe billing write latency and WAL growth.

## Data affected

Cleanup applies to requests at or before the cutoff that lack a modern capture status. It clears the three legacy body columns and marks the body expired. Legacy raw errors become bounded summaries, including embedded errors in recognized upstream diagnostics. Other diagnostic attributes, billing, usage, continuation history and modern captures are preserved. Old diagnostic payloads over 64 KiB or in unrecognized formats are kept and counted under `unchanged_diagnostics` for separate review. Command output contains no bodies, account identifiers or connection details.
