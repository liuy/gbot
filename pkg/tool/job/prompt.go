package job

func jobPrompt() string {
	return `Manages background jobs (background shell, agent, or remote session).

Supports three operations, which can be combined in a single call:
- list: list all background jobs and their statuses
- poll: retrieve output from a running or completed job
  - block=true (default): wait for job completion before returning
  - block=false: non-blocking check of current status
  - timeout: max wait time in ms (default 30000)
- stop: terminate a running job by its ID

Returns a retrieval_status for poll: success, timeout, or not_ready
Returns a status for stop: killed (with the original command/description)
Returns a jobs array for list: each entry has job_id, status, command

Works with all job types: background shells, async agents, and remote sessions`
}
