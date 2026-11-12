import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import type { ExtensionAPI, ExtensionFactory } from "@mariozechner/pi-coding-agent";
import { Type } from "typebox";
import type { SpecMcp } from "./protocol/index.js";

export interface McpTool {
  server: string;
  name: string;
  description: string;
  schema: Record<string, unknown>;
  client: Client;
}

/** connectMcp opens each allowed MCP server through the MCP proxy with the run token (§8.5). */
export async function connectMcp(servers: SpecMcp[], token: string, onError: (server: string, err: unknown) => void): Promise<{ tools: McpTool[]; close: () => Promise<void> }> {
  const tools: McpTool[] = [];
  const clients: Client[] = [];
  for (const s of servers) {
    if (!s.url) continue;
    try {
      const client = new Client({ name: "sayso-agent", version: "1" });
      const transport = new StreamableHTTPClientTransport(new URL(s.url), { requestInit: { headers: { Authorization: `Bearer ${token}` } } });
      await client.connect(transport);
      clients.push(client);
      const list = await client.listTools();
      for (const t of list.tools) {
        tools.push({ server: s.name, name: t.name, description: t.description ?? "", schema: t.inputSchema as Record<string, unknown>, client });
      }
    } catch (e) {
      onError(s.name, e);
    }
  }
  return { tools, close: async () => void (await Promise.allSettled(clients.map((c) => c.close()))) };
}

/** mcpToolName is the pi tool name of an MCP tool. */
export function mcpToolName(server: string, tool: string): string {
  return `mcp__${server}__${tool}`.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 64);
}

/** mcpTools registers the tools of the MCP servers as pi tools. */
export function mcpTools(tools: McpTool[]): ExtensionFactory {
  return (pi: ExtensionAPI) => {
    for (const t of tools) {
      pi.registerTool({
        name: mcpToolName(t.server, t.name),
        label: `${t.server}: ${t.name}`,
        description: `MCP tool ${t.name} of server ${t.server}. ${t.description}`.trim(),
        parameters: Type.Unsafe<Record<string, unknown>>(t.schema),
        async execute(_id, params, signal) {
          const res = await t.client.callTool({ name: t.name, arguments: params }, undefined, { signal });
          const parts = (res.content as { type: string; text?: string }[] | undefined) ?? [];
          const text = parts.map((p) => (p.type === "text" ? (p.text ?? "") : `[${p.type}]`)).join("\n");
          if (res.isError) throw new Error(text || "the MCP tool failed");
          return { content: [{ type: "text" as const, text: text || "(no output)" }], details: {} };
        },
      });
    }
  };
}
