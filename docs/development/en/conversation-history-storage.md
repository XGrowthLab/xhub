# Conversation history storage

The unified Responses endpoint stores conversation history for continuation. Full snapshots can repeat earlier messages across turns. The compact format stores lossless message blocks within the existing caller, public model and endpoint scope, and stores an ordered block index for each response. Larger blocks are compressed and verified by length and digest. Authentication, budgets, native bypass routing and log retention are unchanged.

For compact writes, the gateway checks which message digests are missing within the authenticated scope before encoding and transmitting their blocks. Manifests preserve the full order, including repeated references. The database unique key arbitrates simultaneous inserts.

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
