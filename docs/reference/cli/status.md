---
title: arcave status
description: The planned arcave status command — registered in the command tree, returning not implemented today.
keywords: [arcave status, command tree, docker compose]
---

# arcave status

Planned: a report of the project's configuration and its storage usage.

```text title="Synopsis"
arcave status
```

Today the command is registered and returns:

```text
Error: not implemented
```

Exit code `1`. It carries no flags and no output contract yet, so nothing should be built on it.

What it will eventually report is available now from other commands: [`savepoint list`](savepoint_list.md) prints every record with its `stats` and `images`, including the `stored_bytes` each capture added to the store.

## Next step

[`arcave savepoint list`](savepoint_list.md) — what the store holds today.
