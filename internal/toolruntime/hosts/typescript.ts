/**
 * Tenon's TypeScript tool host.
 *
 * One long-lived process serves every `tools/*.ts` file of one agent project
 * over tenon's line-delimited host protocol on stdin and stdout: one request
 * object per line in, one response object per line out. The protocol is
 * tenon's own, not MCP; the managed MCP boundary lives in tenon and speaks to
 * this host, so authors never write protocol code.
 *
 * Authored contract: each visible `tools/*.ts` file default-exports one object
 * carrying `description`, strict Zod `inputSchema` and `outputSchema`, and
 * `execute`. Zod resolves through the author's own `deno.json` import map;
 * tenon vendors nothing and pins nothing on the author's behalf.
 *
 * Every failure is answered in-band as a bounded `error` string. Nothing is
 * written to stderr on the request path: tenon never forwards host stderr into
 * a model-visible or operator-visible message.
 */
import { z } from "zod";

/** maxLineBytes bounds one request line, matching tenon's own bound. */
const maxLineBytes = 64 * 1024;

/** maxErrorChars bounds one in-band error message. */
const maxErrorChars = 1024;

/** requiredShape is the authored contract, named in every violation. */
const requiredShape =
  "a default export { description: string, inputSchema: strict Zod object schema, " +
  "outputSchema: strict Zod object schema, execute: function }";

interface Tool {
  name: string;
  description: string;
  inputSchema: any;
  outputSchema: any;
  execute: (input: unknown, context: { requestId: string }) => unknown;
}

interface Request {
  id?: unknown;
  method?: unknown;
  params?: { name?: unknown; arguments?: unknown } | null;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** bound trims one message to a single bounded line. */
function bound(text: string): string {
  const flat = text.replace(/[\r\n]+/g, " ");
  return flat.length > maxErrorChars ? flat.slice(0, maxErrorChars) + "..." : flat;
}

function message(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/**
 * fileURL renders an absolute POSIX path as a file URL so an authored tool
 * imports by its real path rather than by a specifier the import map could
 * redirect.
 */
function fileURL(path: string): string {
  return "file://" + path.split("/").map(encodeURIComponent).join("/");
}

/**
 * isStrictObjectSchema reports whether schema is a Zod object schema closed to
 * unknown keys — an object type whose catchall is `never`, which is exactly
 * what `.strict()` and `z.strictObject()` produce. An open schema would let an
 * unvalidated argument reach authored code.
 */
function isStrictObjectSchema(schema: any): boolean {
  const def = schema?._zod?.def ?? schema?.def;
  if (!def || def.type !== "object") return false;
  const catchall = def.catchall;
  const catchallDef = catchall?._zod?.def ?? catchall?.def;
  return catchallDef?.type === "never";
}

/**
 * loadTools imports every visible `tools/*.ts` file in sorted order and
 * validates its default export. Files whose name starts with `_` are shared
 * helper modules: they are never imported as tools. A violation throws,
 * naming the authored file and the required shape.
 */
async function loadTools(sourceDir: string): Promise<Map<string, Tool>> {
  const toolsDir = sourceDir + "/tools";
  const files: string[] = [];
  for await (const entry of Deno.readDir(toolsDir)) {
    if (!entry.isFile) continue;
    if (entry.name.startsWith("_") || !entry.name.endsWith(".ts")) continue;
    files.push(entry.name);
  }
  files.sort();

  const tools = new Map<string, Tool>();
  for (const file of files) {
    const authored = "tools/" + file;
    let module: any;
    try {
      module = await import(fileURL(toolsDir + "/" + file));
    } catch (error) {
      throw new Error(`${authored} could not be imported: ${message(error)}`);
    }
    const declared = module?.default;
    const reject = (why: string) => {
      throw new Error(`${authored} must export ${requiredShape}; ${why}`);
    };
    if (!declared || typeof declared !== "object") {
      reject("it has no default-exported object");
    }
    if (typeof declared.description !== "string" || declared.description.trim() === "") {
      reject("description is not a non-empty string");
    }
    if (!isStrictObjectSchema(declared.inputSchema)) {
      reject("inputSchema is not a strict Zod object schema (call .strict() or use z.strictObject)");
    }
    if (!isStrictObjectSchema(declared.outputSchema)) {
      reject("outputSchema is not a strict Zod object schema (call .strict() or use z.strictObject)");
    }
    if (typeof declared.execute !== "function") {
      reject("execute is not a function");
    }
    // The filename supplies the tool name, with underscores exposed as
    // hyphens; tenon rejects any name it did not discover itself.
    const name = file.slice(0, -".ts".length).replaceAll("_", "-");
    tools.set(name, {
      name,
      description: declared.description,
      inputSchema: declared.inputSchema,
      outputSchema: declared.outputSchema,
      execute: declared.execute,
    });
  }
  return tools;
}

/** catalog renders the list result: every tool with both JSON Schemas. */
function catalog(instanceId: string, tools: Map<string, Tool>): unknown {
  const listed = [];
  for (const tool of tools.values()) {
    listed.push({
      name: tool.name,
      description: tool.description,
      inputSchema: z.toJSONSchema(tool.inputSchema),
      outputSchema: z.toJSONSchema(tool.outputSchema),
    });
  }
  return { instanceId, tools: listed };
}

/**
 * invoke validates the arguments against the authored input schema, runs the
 * tool, and validates its result against the authored output schema. Authored
 * code never sees an argument the schema did not accept.
 */
async function invoke(
  instanceId: string,
  tools: Map<string, Tool>,
  requestId: string,
  params: { name?: unknown; arguments?: unknown } | null | undefined,
): Promise<unknown> {
  const name = params?.name;
  if (typeof name !== "string" || !tools.has(name)) {
    throw new Error("unknown tool");
  }
  const tool = tools.get(name)!;
  const input = tool.inputSchema.parse(params?.arguments);
  const result = await tool.execute(input, { requestId });
  const output = tool.outputSchema.parse(result);
  return { instanceId, output };
}

async function write(value: unknown): Promise<void> {
  let pending = encoder.encode(JSON.stringify(value) + "\n");
  while (pending.length > 0) {
    const written = await Deno.stdout.write(pending);
    pending = pending.subarray(written);
  }
}

/**
 * lines yields one bounded request line at a time. A line over the bound is
 * never truncated and never partially interpreted: the host stops.
 */
async function* lines(): AsyncGenerator<string> {
  let buffer = new Uint8Array(0);
  for await (const chunk of Deno.stdin.readable) {
    const next = new Uint8Array(buffer.length + chunk.length);
    next.set(buffer);
    next.set(chunk, buffer.length);
    buffer = next;
    for (;;) {
      const end = buffer.indexOf(10);
      if (end < 0) break;
      yield decoder.decode(buffer.subarray(0, end));
      buffer = buffer.subarray(end + 1);
    }
    if (buffer.length > maxLineBytes) {
      throw new Error(`one request line exceeded the bounded size of ${maxLineBytes} bytes`);
    }
  }
}

async function main(): Promise<void> {
  const sourceDir = Deno.args[0];
  if (!sourceDir) {
    Deno.exit(2);
  }
  const instanceId = `typescript:${Deno.pid}`;

  // Tools load once, at startup. A violation is remembered and answered in
  // band on every request, so the operator sees the authored file and the
  // required shape instead of a silent exit.
  let tools: Map<string, Tool> | null = null;
  let loadError: string | null = null;
  try {
    tools = await loadTools(sourceDir);
  } catch (error) {
    loadError = message(error);
  }

  for await (const line of lines()) {
    if (line.trim() === "") continue;
    let request: Request;
    try {
      request = JSON.parse(line);
    } catch {
      await write({ id: "", error: "the request line is not one JSON object" });
      continue;
    }
    const id = typeof request.id === "string" ? request.id : "";
    if (loadError !== null || tools === null) {
      await write({ id, error: bound(loadError ?? "no tools were loaded") });
      continue;
    }
    try {
      if (request.method === "list") {
        await write({ id, result: catalog(instanceId, tools) });
      } else if (request.method === "call") {
        await write({ id, result: await invoke(instanceId, tools, id, request.params) });
      } else {
        await write({ id, error: "the host protocol supports list and call" });
      }
    } catch (error) {
      await write({ id, error: bound(message(error)) });
    }
  }
}

await main();
