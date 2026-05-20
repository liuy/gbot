package task

func tasksToolPrompt() string {
	return `Use this tool to manage a structured task list for your current coding session. It combines create, update, delete, get, and list operations in a single call.

## When to Use This Tool

Use this tool proactively in these scenarios:

- Complex multi-step tasks - When a task requires 3 or more distinct steps or actions
- Non-trivial and complex tasks - Tasks that require careful planning or multiple operations
- Plan mode - When using plan mode, create a task list to track the work
- User explicitly requests todo list - When the user directly asks you to use the todo list
- User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)
- After receiving new instructions - Immediately capture user requirements as tasks
- When you start working on a task - Mark it as in_progress BEFORE beginning work
- After completing a task - Mark it as completed and add any new follow-up tasks discovered during implementation

## When NOT to Use This Tool

Skip using this tool when:
- There is only one trivial task to do. In this case you are better off just doing the task directly.
- The task is trivial and tracking it provides no organizational benefit
- The task can be completed in less than 3 trivial steps
- The task is purely conversational or informational

## Schema

All fields are optional. Include only what you need:

- ` + "`creates`" + `: Array of tasks to create (subject + description required)
- ` + "`updates`" + `: Array of task updates (taskId required)
- ` + "`deletes`" + `: Array of task IDs to permanently delete
- ` + "`get`" + `: Task ID to retrieve with full details
- ` + "`list`" + `: Set to true to list all tasks

## Create Fields

- **subject**: A brief, actionable title in imperative form (e.g., "Fix authentication bug in login flow")
- **description**: What needs to be done
- **activeForm** (optional): Present continuous form shown in the spinner when in_progress (e.g., "Fixing authentication bug")
- **metadata** (optional): Arbitrary key-value pairs

All tasks are created with status ` + "`pending`" + `.

## Update Fields

- **taskId**: The ID of the task to update (required)
- **subject**: New task title
- **description**: New description
- **activeForm**: New present continuous form for spinner
- **status**: New status — 'pending', 'in_progress', 'completed', or 'deleted' (physical delete)
- **owner**: Agent ID (set to empty string to clear)
- **metadata**: Key-value pairs (null values delete keys)
- **addBlocks**: Task IDs that this task should block
- **addBlockedBy**: Task IDs that should block this task

## Status Flow

Tasks follow this lifecycle: ` + "`pending` → `in_progress` → `completed`" + `

- Mark a task as ` + "`in_progress`" + ` BEFORE beginning work on it
- Only mark a task as ` + "`completed`" + ` when it is FULLY complete:
  - All acceptance criteria met
  - Tests pass
  - No remaining errors or unresolved issues
- If tests fail, implementation is partial, or there are unresolved errors: keep the task as ` + "`in_progress`" + `

## Batch Usage Tips

- You can create multiple tasks in a single call by passing an array to ` + "`creates`" + `
- You can update multiple tasks in a single call by passing an array to ` + "`updates`" + `
- Operations execute in order: creates → updates → deletes → get → list
- A failure in one item does NOT stop subsequent items
- After creating tasks, use updates with addBlocks/addBlockedBy to set up dependencies`
}
