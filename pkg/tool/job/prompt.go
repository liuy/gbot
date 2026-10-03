package job

func jobPrompt() string {
	return `Manages background jobs (background shell, agent, or remote session).

Actions (one per call, set via the "action" parameter):
- list: list all background jobs and their statuses
- poll: retrieve output from a running or completed job (requires job_id)
  - block=true (default): wait for job completion before returning
  - block=false: non-blocking check of current status
  - timeout: max wait time in ms (default 30000)
- stop: terminate a running job by its ID (requires job_id)

job_id values come from Job list output (e.g. "bg-1"); do not guess them.

Returns a retrieval_status for poll: success, timeout, or not_ready
Returns a status for stop: killed (with the original command/description)
Returns one line per job for list: job_id=... status=... command=...

Works with all job types: background shells, async agents, and remote sessions`
}
