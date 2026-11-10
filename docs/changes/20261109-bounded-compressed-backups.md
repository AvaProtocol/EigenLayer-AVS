# Bounded compressed application backups

Application backups now stream Badger's full backup directly into gzip at its
fast compression level. No uncompressed intermediate copy is created. This
applies to both periodic backups and the backup required before migrations.
Periodic scheduling is unchanged; `backup_interval_hours: 0` disables only the
periodic timer, preserving migration backups and Railway-managed snapshots.

## Publication and failure handling

Each attempt uses a unique private `.partial-*` directory on the backup
filesystem. The gzip stream must finish, and the file must sync and close
successfully. The directory is synced, renamed atomically to its completed
name, and its parent synced before the backup is reported successful.
The ancestor chain within the backup filesystem is synced on every attempt,
including retries and restarts after a failed sync left directories in place.
Migration and periodic calls through the same service are serialized.

Failed attempts are removed and do not count toward retention. The newest
pre-existing recognized backup remains protected until a replacement is
durably published. A pre-migration backup failure still stops the migration;
there is no automatic bypass. Abrupt process termination can leave a hidden
partial directory; it is ignored by retention and must be inspected during
offline maintenance, since another process might still own it.

## Storage limits

Optional gateway configuration (zero or omitted means the shown default;
negative values are invalid):

```yaml
backup_retention_count: 3
backup_retention_max_bytes: 12884901888 # 12 GiB
backup_min_free_bytes: 2147483648       # 2 GiB
```

The byte budget covers recognized completed backups plus the current compressed
file, including its gzip footer. Free space is rechecked before every compressed
write. Older backups may be removed before or during writing to enforce the
count/byte limits or preserve free space; the final existing backup is never
removed to make room. Count one therefore allows the old and new copies to
coexist temporarily until publication. If both copies cannot fit in the byte
budget, the attempt fails and preserves the old one. Increasing the configured
budget or separately compressing a large legacy backup may be required.

Limits apply to application backup files, not live database growth. The free
space check is a guard against this writer exhausting the filesystem, not a
filesystem reservation that excludes concurrent database or external writes.
An already oversized protected backup is retained and causes new attempts to
fail until space/budget is addressed.

Legacy minute timestamp directories and new UTC timestamp/unique-suffix
directories participate in retention. Symlinks, empty files, partial directories,
and directories with unknown contents are preserved and excluded. Recognizing
a legacy completed filename does not prove an old backup's integrity; verify
recovery copies through a restore before relying on them.

## Restore and compatibility

Durable application backup creation is supported on Linux and macOS. Other
platforms, including Windows, return an explicit unsupported-platform error
before creating or pruning backups. Windows operator/client binaries and
raw/gzip recovery remain available; a Windows gateway with pending migrations
or enabled periodic backups refuses the unsupported operation. This preserves
the durability requirement rather than silently skipping directory sync.

New backups contain `full-backup.db.gz`. `backup.Open` selects raw format for
`.db` files and gzip for `.gz` files. Other names use gzip-header detection,
rewinding to raw format on header failure only when the Badger batch length
fits the file. A raw batch length can mimic even a complete valid gzip header;
use a `.db` filename to select raw format unambiguously. Files named `.gz` do
not fall back to raw format on corruption. Consume gzip through EOF to
verify its checksum. The retired-wallet-funds tool supports both formats and
checks the trailer before accepting a restore. Its restore database remains
temporary, separate from the running gateway.

Existing raw backups and the manually compressed production backup remain
readable. Older restore tools can use `gzip -dc full-backup.db.gz` to obtain a
raw stream or a scratch `full-backup.db` file. Never restore into a live DB.
An older gateway image does not enforce these limits or recognize the new
directory names for pruning; account for that during rollback.

There are no database key/model, protobuf or public REST changes. The new
limits default safely without required configuration or new credentials.

## Verification

Tests restore real raw/compressed Badger streams and compare stored binary
values; exercise CLI restoration of both formats and corrupt-checksum cleanup;
inject source, write, close, sync and publication failures; exhaust byte/free
space limits during writing; preserve the last existing backup; exercise
count/byte retention, unknown/symlink entries, clock rollback and concurrent
backup calls; retry ancestor syncs in the same and restarted service; restore
real protobuf batches whose length mimics gzip headers; and prove migrations
do not execute after backup failure.
