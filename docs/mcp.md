# MCP

The agent can use tools of MCP servers, for example a service catalogue or an internal documentation server. Sayso runs on your infrastructure, so it can reach internal MCP servers.

The agent never connects to an MCP server directly. It connects to the MCP proxy in the API with its run token. The proxy holds the server credential.

## Add a server

Admins add servers on Settings → MCP. A server has these fields:

| Field | Use |
|---|---|
| `name` | Lower-case letters, digits and hyphens. The proxy route uses it. |
| `url` | The Streamable HTTP URL of the server. |
| `headers` | Headers that the proxy sends on each upstream request. |
| `secret_header` | The name of the header that carries the secret value, for example `Authorization`. |
| secret | The value of `secret_header`. Type a value, or give the name of an environment variable. |
| `allow_write` | Lets the allowlist include tools that are not read-only. Default off. |
| `allow_tools` | Name patterns of other tools to allow. They need `allow_write`. |

Click **Test** to connect to the server and list its tools. Sayso stores each tool with its annotations and shows whether the allowlist allows it.

## Tool allowlist

The proxy applies this rule to each tool:

1. A tool with the annotation `readOnlyHint: true` is allowed.
2. Any other tool is blocked, unless the server has `allow_write` on and a pattern in `allow_tools` matches the tool name.

Patterns use glob syntax: `*` matches any run of characters, `?` matches one character, and `[...]` matches a character class.

This server entry is in the format of `sayso config export`:

```yaml
name: catalog
url: https://catalog.example.test/mcp
secret_header: Authorization
allow_write: true
allow_tools:
  - "annotate_*"
```

In this example, the agent can use all read-only tools and the tools whose names start with `annotate_`.

## Choose servers for a repository

Each repository has a list of MCP servers. A maintainer sets the list on the repository page. A new repository has no servers.

`.sayso.yml` on the base branch can narrow the list. It cannot add a server:

```yaml
mcp: [catalog]
```

A name in the file that is not in the repository list is ignored, and the round page shows a warning. `mcp: []` turns off all servers for the repository.

The repository page shows the effective list and the source of each value.

## MCP proxy

The agent runtime gets one entry for each allowed server, with the URL `${SAYSO_RUNNER_API_URL}/mcp/<name>` and the header `Authorization: Bearer <run token>`.

For each request, the proxy does these steps:

1. It accepts only an active run token.
2. It accepts only a server that the run and its repository may use.
3. It opens one upstream session for each run and server, with the stored headers and the secret.
4. It removes blocked tools from `tools/list`.
5. It refuses `tools/call` for blocked tools.
6. It writes an `mcp_call` event for each call, with the tool name, the duration, the status and a redacted preview.

Sayso closes the upstream sessions of runs that ended every minute.

The run page shows each MCP call on the timeline.

## Diagnostics

`sayso doctor` and the Diagnostics page check each MCP server. See [operations.md](operations.md#sayso-doctor).
