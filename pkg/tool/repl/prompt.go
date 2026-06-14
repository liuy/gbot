package repl

// replDescription is the short tool description for the API tool definition.
// LLM sees this when deciding whether to use REPL.
// Detailed API reference and examples live in toolPrompt (system prompt contribution).
const replDescription = `Run JavaScript code to orchestrate tool calls and data processing.
Evaluates ES6+ with async/await support. Call any tool via tool(name, args).
Supports console.log, setTimeout/clearTimeout. Use globalThis for cross-call data persistence. Timeout via // @timeout: ms pragma.`

// toolPrompt is the system prompt contribution for the REPL tool.
// Provides full API reference, examples, and session management details.
const toolPrompt = `JavaScript execution environment for orchestrating the tools.

Execute JavaScript (ES6+) code with access to the tools via the tool() API. Use this for complex multi-step operations, data transformation, and tool orchestration that would be awkward with individual tool calls.

## API Reference

### tool(name, args) → string (synchronous)
Call any tool by name. Returns the tool's output as a string (NOT a Promise).
- Throws on error: wrap in try/catch to handle failures.
- args can be an object: tool("Read", {file_path: "/path/to/file"}) or a JSON string
- SYNCHRONOUS — blocks until tool completes. Do NOT chain .then() or .catch().
- For parallel calls, use Promise.all([tool(...), tool(...)]) with await.

### console.log(msg)
Output is captured and returned as the tool result (not written to stdout).
Use console.log() to report results, progress, and intermediate values.

### setTimeout(callback, delayMs) → id
Schedule a callback to run after delayMs milliseconds. Returns a timer reference (truthy, unique per call).
- Callbacks fire asynchronously but are drained before execution completes — output appears in the tool result.
- clearTimeout(id) cancels a pending timer.
- Timers execute in order of their delay; new timers registered from callbacks are also drained.

### cwd (global variable)
The current working directory, available as a global string.

## Timeout

// @timeout: ms — optional pragma on the first line to set execution timeout.
Default: 120000ms (120s). Range: 1000-600000ms (1s to 10min).
Timeout is wall-clock time including tool() wait — time spent waiting for tool() responses (e.g., permission prompts) counts.

## ES6+ Support

const/let, arrow functions, template literals, classes, async/await, Promise — all supported.
Top-level await works: const data = await Promise.resolve(42);

## Session Management

Scripts execute within a session backed by a persistent JavaScript VM. Variable declarations (var/let/const) are scoped to each execution and do NOT persist across calls. To persist data across calls, assign to globalThis:
- globalThis.x = 42 — save value
- globalThis.x — retrieve value (undefined if not set)
- Properties on globalThis survive across executions within the same session.
Use reset: true to clear the session and start fresh.

## Examples

// Batch file reads
const files = ["/path/a.txt", "/path/b.txt", "/path/c.txt"];
for (const f of files) {
  try {
    const content = tool("Read", {file_path: f});
    console.log(f + ": " + content.split("\n").length + " lines");
  } catch (e) {
    console.log(f + ": " + e);
  }
}

// Parallel tool calls with Promise.all
const [globResult, grepResult] = await Promise.all([
  tool("Grep", {glob: "**/*.go"}),
  tool("Grep", {pattern: "TODO"})
]);

// Data processing with cross-call persistence
const lines = grepResult.split("\n").filter(l => l.trim());
globalThis.todoCount = lines.length;
console.log("Found " + lines.length + " TODOs");
// Later calls can access: globalThis.todoCount

// RLM pattern: REPL filters + Agent classifies semantically + REPL aggregates
// E.g. "Among users 101-200, how many entries ask about a person?"
const data = tool("Read", {file_path: "entries.txt"});
const filtered = data.split("\n").filter(l => /^User: (1\d{2}|200)\b/.test(l));
console.log("Filtered to " + filtered.length + " entries");
// Partition into chunks, classify each with Agent, sum results
const chunks = [];
for (let i = 0; i < filtered.length; i += 50) chunks.push(filtered.slice(i, i + 50));
let total = 0;
for (const chunk of chunks) {
  const result = tool("Agent", {
    prompt: "Count how many of these entries ask about a specific person. Reply with ONLY a number:\n" + chunk.join("\n")
  });
  total += parseInt(result) || 0;
}
console.log("Entries about a person: " + total);
`
