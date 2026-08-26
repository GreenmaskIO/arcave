---
title: arcave mcp
description: Serve arcave's savepoint workflows to an AI agent as MCP tools over stdio — registration, the stdout contract, and what one server can address.
keywords: [arcave mcp, MCP server, AI agent, model context protocol, stdio, docker compose]
---

# arcave mcp

Serves arcave's workflows as MCP tools.

```text title="Synopsis"
arcave mcp
```

No arguments and no flags of its own. The four tools, their arguments and their output shapes are in [MCP tools](../mcp_tools.md).

`arcave mcp` takes no `-p/--project-name`: one server addresses many compose projects, and each tool call names the one it means. It builds nothing at startup — every call wires the store and the daemon clients for the project it names and releases them when the call returns.

## Transport

JSON-RPC 2.0 over stdio. The agent host launches `arcave mcp` as a child process and speaks to it over the standard streams: no HTTP server, no port, no network configuration.

`arcave mcp` writes **nothing** to stdout — that stream is the protocol channel. Logs still go to stderr in whatever `--log-format` says, so a host can surface them without corrupting the protocol.

## Registration

```json title=".mcp.json"
{
  "mcpServers": {
    "arcave": {
      "command": "arcave",
      "args": ["mcp"]
    }
  }
}
```

Start the server **in the project directory**: `.arcave/config.yaml` is resolved against the working directory exactly as it is for the CLI.

## Errors

| Condition | What happens |
|---|---|
| No `.arcave/config.yaml` | The server refuses to start: `config is not found: run 'arcave init' if not yet initialized` |
| A call names a compose project that is not in the daemon | The call fails; the server stays up. |
| Another arcave run holds the project lock | The call fails at once rather than queuing. |
| An interrupted rollback is marked | Mutating calls are refused until it is resolved. |

A tool call runs the same workflow the CLI runs, under the same rules: the project is stopped and started again, the project lock is claimed, and a failed rollback replays its safety snapshot. A savepoint an agent takes is listed and applied by the CLI, and the reverse.

## Examples

```shell title="Run it by hand to inspect a host integration"
arcave mcp
```

It sits waiting for JSON-RPC on stdin. Normally the host launches it.

```shell title="Keep a record of what an agent did"
arcave --log-format json mcp 2> arcave-mcp.log
```

## Next step

[MCP tools](../mcp_tools.md) — the four tools, their arguments and what they return.
