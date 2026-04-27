package tasks

// taskCreatePrompt returns the system prompt for the TaskCreate tool.
// Source: tools/TaskCreateTool/prompt.ts
func taskCreatePrompt() string {
	return `Use this tool to create a structured task list for your current coding session. This helps you track progress, organize complex tasks, and demonstrate thoroughness to the user.
It also helps the user understand the progress of the task and overall progress of their requests.

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

## Task Fields

- **subject**: A brief, actionable title in imperative form (e.g., "Fix authentication bug in login flow")
- **description**: What needs to be done
- **activeForm** (optional): Present continuous form shown in the spinner when the task is in_progress (e.g., "Fixing authentication bug"). If omitted, the spinner shows the subject instead.

All tasks are created with status ` + "`pending`" + `.

## Tips

- Create tasks with clear, specific subjects that describe the outcome
- After creating tasks, use TaskUpdate to set up dependencies (blocks/blockedBy) if needed
- Check TaskList first to avoid creating duplicate tasks`
}

// taskGetPrompt returns the system prompt for the TaskGet tool.
// Source: tools/TaskGetTool/prompt.ts
func taskGetPrompt() string {
	return `Use this tool to retrieve a task by its ID from the task list.

## When to Use This Tool

- When you need the full description and context before starting work on a task
- To understand task dependencies (what it blocks, what blocks it)
- After being assigned a task, to get complete requirements

## Output

Returns full task details:
- **subject**: Task title
- **description**: Detailed requirements and context
- **status**: 'pending', 'in_progress', or 'completed'
- **blocks**: Tasks waiting on this one to complete
- **blockedBy**: Tasks that must complete before this one can start

## Tips

- After fetching a task, verify its blockedBy list is empty before beginning work.
- Use TaskList to see all tasks in summary form.`
}

// taskListPrompt returns the system prompt for the TaskList tool.
// Source: tools/TaskListTool/prompt.ts
func taskListPrompt() string {
	return `Use this tool to list all tasks in the current task list.

## Output

Returns a list of all tasks with summary information:
- **id**: Task ID
- **subject**: Brief task title
- **status**: 'pending', 'in_progress', or 'completed'
- **owner**: Agent assigned to the task (if any)
- **blockedBy**: IDs of tasks blocking this one (only uncompleted blockers shown)

## Tips

- Prioritize tasks in ID order (lower ID first, as earlier tasks often set up context for later ones)
- Find tasks with status 'pending', no owner, and empty blockedBy list to claim
- After completing a task, call TaskList to find the next available task`
}

// taskUpdatePrompt returns the system prompt for the TaskUpdate tool.
// Source: tools/TaskUpdateTool/prompt.ts
func taskUpdatePrompt() string {
	return `Use this tool to update an existing task in the task list.

## Status Flow

Tasks follow this lifecycle: ` + "`pending` → `in_progress` → `completed`" + `

## Key Rules

- Mark a task as ` + "`in_progress`" + ` BEFORE beginning work on it
- Only mark a task as ` + "`completed`" + ` when it is FULLY complete:
  - All acceptance criteria met
  - Tests pass
  - No remaining errors or unresolved issues
- If tests fail, implementation is partial, or there are unresolved errors: keep the task as ` + "`in_progress`" + `
- Use ` + "`status='deleted'`" + ` to permanently delete a task
- Use ` + "`metadata`" + ` with null values to delete specific keys
- Before updating, use TaskGet to read the latest task state

## Field Updates

- **subject**: New task title
- **description**: New description
- **activeForm**: New present continuous form for spinner
- **status**: New status ('pending', 'in_progress', 'completed', or 'deleted')
- **owner**: Agent ID (set to empty string to clear)
- **metadata**: Key-value pairs (null values delete keys)
- **addBlocks**: Task IDs that this task should block
- **addBlockedBy**: Task IDs that should block this task`
}
