package job

func jobOutputPrompt() string {
	return `- Retrieves output from a running or completed background job (background shell, agent, or remote session)
- Takes a task_id parameter identifying the job
- Returns the job output along with status information
- Use block=true (default) to wait for job completion
- Use block=false for non-blocking check of current status
- Job IDs can be found using the /tasks command
- Works with all job types: background shells, async agents, and remote sessions`
}

func jobStopPrompt() string {
	return `- Stops a running background job by its ID
- Takes a task_id parameter identifying the job to stop
- Returns a success or failure status
- Use this tool when you need to terminate a long-running job`
}
