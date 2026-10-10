package task

func tasksToolPrompt() string {
	return `Use this tool to manage a structured task list for your current coding session. It combines create, update, delete, get, and list operations behind an "action" parameter: exactly one operation per call.

## When to Use This Tool

Use this tool proactively in these scenarios:

- Complex multi-step tasks - When a task requires 3 or more distinct steps or actions
- Non-trivial and complex tasks - That require careful planning or multiple operations
- Plan mode - When using plan mode, create a task list to track the work
- User explicitly requests todo list - When the user directly asks you to use the todo list
- User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)
- After receiving new instructions - Immediately capture user requirements as tasks
- When starting work on a task - Mark a task as in_progress BEFORE beginning work
- After completing a task - Mark a task as completed and add any new follow-up tasks discovered during implementation

## When NOT to Use This Tool

Skip using this tool when:
- There is only one trivial task to do. In this case you are better off just doing the task directly.
- The task is trivial and tracking it provides no organizational benefit
- The task can be completed in less than 3 trivial steps
- The task is purely conversational or informational

## Actions (one per call, set via the "action" parameter)

- create: create a task (requires subject + description; optional activeForm, metadata)
- update: modify a task (requires taskId; optional subject, description, activeForm, status, owner, metadata, addBlocks, addBlockedBy)
- delete: permanently delete tasks (requires taskIds; accepts multiple IDs in one call)
- get: retrieve one task with full details (requires taskId)
- list: list all tasks (takes no other parameters)

## Create Fields

- subject: a brief, actionable title in imperative form (e.g., "Fix authentication bug in login flow")
- description: what needs to be done
- activeForm (optional): present continuous form shown in the spinner when in_progress (e.g., "Fixing authentication bug")
- metadata (optional): arbitrary key-value pairs

All tasks are created with status "pending".

## Update Fields

- taskId: the ID of the task to update (required)
- subject / description / activeForm: new values
- status: "pending", "in_progress", or "completed"
- owner: agent ID (empty string clears)
- metadata: key-value pairs (null values delete keys)
- addBlocks: task IDs that this task should block
- addBlockedBy: task IDs that should block this task

## Status Flow

Tasks follow this lifecycle: "pending" → "in_progress" → "completed"

- Mark a task as "in_progress" BEFORE beginning work on it
- Only mark a task as "completed" when it is FULLY complete:
  - All acceptance criteria met
  - Tests pass
  - No remaining errors or unresolved issues
- If tests fail, implementation is partial, or there are unresolved errors: keep the task as "in_progress"

## Usage Tips

- One action per call; issue separate Task calls to create or update several tasks
- delete accepts multiple task IDs in one call
- To set up dependencies, create both tasks, then update one with addBlocks/addBlockedBy
- Use {"action":"delete"} to remove tasks — status "deleted" does not exist`
}
