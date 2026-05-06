package repl

// replDescription is the short tool description for the API tool definition.
// LLM sees this when deciding whether to use REPL.
// Detailed API reference and examples live in toolPrompt (system prompt contribution).
const replDescription = `Run JavaScript code to orchestrate tool calls and data processing.
Evaluates ES6+ with async/await support. Call any gbot tool via tool(name, args).
Supports console.log, store/load for cross-call persistence, setTimeout, and exit().
Variables do NOT persist across calls (function-scoped). Use store()/load() for cross-call data. Session store/load state persists; use reset: true to clear. Timeout via // @timeout: ms pragma.`

// toolPrompt is the system prompt contribution for the REPL tool.
// Provides full API reference, examples, and session management details.
const toolPrompt = `REPL — JavaScript execution environment for orchestrating gbot tools.

Execute JavaScript (ES6+) code with access to gbot tools via the tool() API. Use this for complex multi-step operations, data transformation, and tool orchestration that would be awkward with individual tool calls.

## API Reference

### tool(name, args) → string
Call any gbot tool by name. Returns the tool's output as a string.
- Check for errors: if the result starts with "ERROR:", the tool call failed.
- args can be an object: tool("Read", {file_path: "/path/to/file"}) or a JSON string
- tool() is synchronous from JS perspective (blocks until tool completes).

### console.log(msg)
Output is captured and returned as the tool result (not written to stdout).
Use console.log() to report results, progress, and intermediate values.

### exit()
Immediately end the script. Output collected so far is returned.

### store(key, value) / load(key) → value
Persist data across multiple tool calls within the same session.
- store("key", "value") saves the value
- load("key") retrieves it (returns undefined if not found)
- Values survive across multiple code executions in the same session

### notify(value)
Send a progress notification that appears in the output.

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

Scripts execute within a session that persists across calls. store/load data survives between executions. Variables are function-scoped and do NOT persist — use store()/load() for cross-call data. Use reset: true to clear the session.

## Examples

// Batch file reads
const files = ["/path/a.txt", "/path/b.txt", "/path/c.txt"];
for (const f of files) {
  const content = tool("Read", {file_path: f});
  if (!content.startsWith("ERROR:")) {
    console.log(f + ": " + content.split("\n").length + " lines");
  }
}

// Parallel tool calls with Promise.all
const [globResult, grepResult] = await Promise.all([
  tool("Glob", {pattern: "**/*.go"}),
  tool("Grep", {pattern: "TODO"})
]);

// Data processing with persistence
const lines = grepResult.split("\n").filter(l => l.trim());
store("todoCount", lines.length);
console.log("Found " + lines.length + " TODOs");
// Later calls can: load("todoCount")
`
