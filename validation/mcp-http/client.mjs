// A client of the official TypeScript SDK of MCP, over streamable HTTP, with the token.
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const url = new URL(process.argv[2]);
const token = process.env.METAGENTE_TOKEN ?? "";
async function connect(withToken) {
  const headers = withToken ? { Authorization: `Bearer ${token}` } : {};
  const client = new Client({ name: "ts-check", version: "1.0.0" });
  const transport = new StreamableHTTPClientTransport(url, { requestInit: { headers } });
  await client.connect(transport);
  return { client, transport };
}
const show = (r) => JSON.stringify(r.content.map((c) => c.text)) + (r.isError ? " (isError)" : "");

const { client, transport } = await connect(true);
console.log("session:", transport.sessionId ? "issued" : "none", "| server:", JSON.stringify(client.getServerVersion()));
const { tools } = await client.listTools();
console.log("tools:", tools.map((t) => t.name).join(", "));
console.log("greet Maria:", show(await client.callTool({ name: "Hello__greet", arguments: { name: "Maria" } })));
console.log("remember Ana:", show(await client.callTool({ name: "Notes__remember", arguments: { what: "Ana" } })));
console.log("recall, same session:", show(await client.callTool({ name: "Notes__recall", arguments: {} })));
const other = await connect(true);
console.log("recall, another session:", show(await other.client.callTool({ name: "Notes__recall", arguments: {} })));
console.log("missing value:", show(await client.callTool({ name: "Hello__greet", arguments: {} })).slice(0, 160));
await transport.terminateSession();
console.log("terminated: ok");
await client.close();
await other.transport.terminateSession();
await other.client.close();
try {
  await connect(false);
  console.log("without token: CONNECTED (wrong)");
} catch (e) {
  console.log("without token: refused:", String(e.message ?? e).slice(0, 120));
}
