package repl

// replDescription is the short tool description for the API tool definition.
// LLM sees this when deciding whether to use REPL.
// Detailed API reference and examples live in toolPrompt (system prompt contribution).
const replDescription = `Run JavaScript code to orchestrate tool calls and data processing.
Evaluates ES6+ with async/await support. Call tools via tools.Name(args); JSON results arrive as objects.
Supports console.log, setTimeout/clearTimeout, image(path) evidence. Use globalThis for cross-call data persistence. Timeout via // @timeout: ms pragma.`

// toolPrompt is the system prompt contribution for the REPL tool.
// Provides full API reference, examples, and session management details.
const toolPrompt = `JavaScript execution environment for orchestrating the tools.

Write JavaScript (ES6+) code with access to every tool via the global tools object. Use it for complex multi-step operations, data transformation, and tool orchestration that would be awkward with individual calls.

## Discovering tools

ALL_TOOLS is a global array of {name, description} entries — inspect it when unsure what is callable:
for (const t of ALL_TOOLS) console.log(t.name + ": " + t.description);

## Calling tools

Every tool is a function property on the global tools object; the callable name is the one listed in ALL_TOOLS (e.g. tools.Bash, tools.Read, tools.mcp__github__create_issue).

- Pass arguments as an object: tools.Read({file_path: "/path/to/file"}) — or as a pre-encoded JSON string.
- SYNCHRONOUS — returns the tool result directly, NOT a Promise. Do NOT chain .then() or .catch().
- If the result is valid JSON you get the parsed object/array; otherwise you get the raw string.
- Throws on error: wrap in try/catch to handle failures.
- For parallel calls, use Promise.all with await: const [a, b] = await Promise.all([tools.Glob({...}), tools.Grep({...})]);

### console.log(msg)
Output is captured and returned as the tool result (not written to stdout).
Use console.log() to report results, progress, and intermediate values.

### image(path)
Attach a local image file (screenshot, chart, rendered UI) as visual evidence returned with the result. Use it when a final state must be judged visually — e.g. a screenshot after a UI-automation loop terminates. Missing or unreadable files add a warning line instead of failing. At most 8 images per execution; extras are dropped with a warning.

### setTimeout(callback, delayMs) → id
Schedule a callback to run after delayMs milliseconds. Callbacks fire asynchronously but are drained before execution completes — their output appears in the result. clearTimeout(id) cancels a pending timer.

### cwd (global variable)
The current working directory, available as a global string.

## Timeout

// @timeout: ms — optional pragma on the first line to set execution timeout.
Default: 120000ms (120s). Range: 1000-600000ms (1s to 10min).
Timeout is wall-clock time including tool call waits — time spent waiting for responses (e.g., permission prompts) counts.

## Session Management

The VM persists for the session. Variable declarations (var/let/const) are scoped to each execution and do NOT persist across calls. Assign to globalThis to persist data:
- globalThis.x = 42 — save value
- globalThis.x — retrieve value (undefined if not set)
Use reset: true to clear the session and start fresh.

## Examples

// Batch file reads
const files = ["/path/a.txt", "/path/b.txt", "/path/c.txt"];
for (const f of files) {
  try {
    const content = tools.Read({file_path: f});
    console.log(f + ": " + content.split("\n").length + " lines");
  } catch (e) {
    console.log(f + ": " + e);
  }
}

// Parallel calls with Promise.all
const [globResult, grepResult] = await Promise.all([
  tools.Glob({pattern: "**/*.go"}),
  tools.Grep({pattern: "TODO"})
]);

// JSON output arrives as a real object — index fields directly
const listed = tools.Glob({pattern: "*.go"});
console.log("glob returned:", typeof listed);

// Data processing with cross-call persistence
globalThis.todoCount = grepResult.split("\n").length;
console.log("Found " + globalThis.todoCount + " TODOs");
// Later executions can access: globalThis.todoCount

// RLM pattern: REPL filters + Agent classifies semantically + REPL aggregates
// E.g. "Among users 101-200, how many entries ask about a person?"
const data = tools.Read({file_path: "entries.txt"});
const filtered = data.split("\n").filter(l => /^User: (1\d{2}|200)\b/.test(l));
console.log("Filtered to " + filtered.length + " entries");
// Partition into chunks, classify each with Agent, sum results
const chunks = [];
for (let i = 0; i < filtered.length; i += 50) chunks.push(filtered.slice(i, i + 50));
let total = 0;
for (const chunk of chunks) {
  const result = tools.Agent({
    prompt: "Count how many of these entries ask about a specific person. Reply with ONLY a number:\n" + chunk.join("\n")
  });
  total += parseInt(result) || 0;
}
console.log("Entries about a person: " + total);

// Attach a screenshot as evidence once a UI-automation loop reaches its final state
tools.Computer({action: "screenshot"});
image("/tmp/final-state.png");
`
