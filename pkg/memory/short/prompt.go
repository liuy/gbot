package short

import (
	"fmt"
	"regexp"
	"strings"
)

// NO_TOOLS_PREAMBLE provides explicit instruction to NOT use tools.
// Critical for preventing wasted turns on adaptive-thinking models.
const noToolsPreamble = `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.

- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.
- You already have all the context you need in the conversation above.
- Tool calls will be REJECTED and will waste your only turn — you will fail the task.
- Your entire response must be plain text: an <analysis> block followed by a <summary> block.

`

// detailedAnalysisInstructionBase is for full conversation compaction.
const detailedAnalysisInstructionBase = `Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like:
     - file names
     - full code snippets
     - function signatures
     - file edits
   - Errors that you ran into and how you fixed them
   - Pay special attention to specific user feedback that you received, especially if you told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.`

// detailedAnalysisInstructionPartial is for partial/recent compaction.
const detailedAnalysisInstructionPartial = `Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Analyze the recent messages chronologically. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like:
     - file names
     - full code snippets
     - function signatures
     - file edits
   - Errors that you ran into and how you fixed them
   - Pay special attention to specific user feedback that you received, especially if you told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.`

// baseCompactPrompt is the full conversation compaction template.
const baseCompactPrompt = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

` + detailedAnalysisInstructionBase + `

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents in detail
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Pay special attention to the most recent messages and include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List all errors that you ran into, and how you fixed them. Pay special attention to specific user feedback that you received, if you told you to do something differently.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results. These are critical for understanding the users' feedback and changing intent.
7. Pending Tasks: Outline any pending tasks that you have explicitly been asked to work on.
8. Current Work: Describe in detail precisely what was being worked on immediately before this summary request, paying special attention to the most recent messages from both user and assistant. Include file names and code snippets where applicable.
9. Optional Next Step: List the next step that you will take that is related to the most recent work you were doing. IMPORTANT: ensure that this step is DIRECTLY in line with the user's most recent explicit requests, and the task you were working on immediately before this summary request. If your last task was concluded, then only list next steps if they are explicitly in line with the users request. Do not start on tangential requests or really old requests that were already completed without confirming with the user first.
                       If there is a next step, include direct quotes from the most recent conversation showing exactly what task you were working on and where you left off. This should be verbatim to ensure there's no drift in task interpretation.

Here's an example of how your output should be structured:

<example>
<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

2. Key Technical Concepts:
   - [Concept 1]
   - [Concept 2]
   - [...]

3. Files and Code Sections:
   - [File Name 1]
      - [Summary of why this file is important]
      - [Summary of the changes made to this file, if any]
      - [Important Code Snippet]
   - [File Name 2]
      - [Important Code Snippet]
   - [...]

4. Errors and fixes:
    - [Detailed description of error 1]:
      - [How you fixed it]
      - [User feedback on the error if any]
    - [...]

5. Problem Solving:
   [Description of solved problems and ongoing troubleshooting]

6. All user messages:
    - [Detailed non tool use user message]
    - [...]

7. Pending Tasks:
   - [Task 1]
   - [Task 2]
   - [...]

8. Current Work:
   [Precise description of current work]

9. Optional Next Step:
   [Optional Next step to take]

</summary>
</example>

Please provide your summary based on the conversation so far, following this structure and ensuring precision and thoroughness in your response.

There may be additional summarization instructions provided in the included context. If so, remember to follow these instructions when creating the above summary. Examples of instructions include:
<example>
## Compact Instructions
When summarizing the conversation focus on typescript code changes and also remember the mistakes you made and how you fixed them.
</example>

<example>
# Summary instructions
When you are using compact - please focus on test output and code changes. Include file reads verbatim.
</example>
`

// partialCompactPrompt is for "from" direction - summarize recent messages after retained context.
const partialCompactPrompt = `Your task is to create a detailed summary of the RECENT portion of the conversation — the messages that follow earlier retained context. The earlier messages are being kept intact and do NOT need to be summarized. Focus your summary on what was discussed, learned, and accomplished in the recent messages only.

` + detailedAnalysisInstructionPartial + `

Your summary should include the following sections:

1. Primary Request and Intent: Capture the user's explicit requests and intents from the recent messages
2. Key Technical Concepts: List important technical concepts, technologies, and frameworks discussed recently.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List errors encountered and how they were fixed.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages from the recent portion that are not tool results.
7. Pending Tasks: Outline any pending tasks from the recent messages.
8. Current Work: Describe precisely what was being worked on immediately before this summary request.
9. Optional Next Step: List the next step related to the most recent work. Include direct quotes from the most recent conversation.

Here's an example of how your output should be structured:

<example>
<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

2. Key Technical Concepts:
   - [Concept 1]
   - [Concept 2]

3. Files and Code Sections:
   - [File Name 1]
      - [Summary of why this file is important]
      - [Important Code Snippet]

4. Errors and fixes:
    - [Error description]:
      - [How you fixed it]

5. Problem Solving:
   [Description]

6. All user messages:
    - [Detailed non tool use user message]

7. Pending Tasks:
   - [Task 1]

8. Current Work:
   [Precise description of current work]

9. Optional Next Step:
   [Optional Next step to take]

</summary>
</example>

Please provide your summary based on the RECENT messages only (after the retained earlier context), following this structure and ensuring precision and thoroughness in your response.
`

// partialCompactUpToPrompt is for "up_to" direction - summary precedes newer messages.
const partialCompactUpToPrompt = `Your task is to create a detailed summary of this conversation. This summary will be placed at the start of a continuing session; newer messages that build on this context will follow after your summary (you do not see them here). Summarize thoroughly so that someone reading only your summary and then the newer messages can fully understand what happened and continue the work.

` + detailedAnalysisInstructionBase + `

Your summary should include the following sections:

1. Primary Request and Intent: Capture the user's explicit requests and intents in detail
2. Key Technical Concepts: List important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List errors encountered and how they were fixed.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results.
7. Pending Tasks: Outline any pending tasks.
8. Work Completed: Describe what was accomplished by the end of this portion.
9. Context for Continuing Work: Summarize any context, decisions, or state that would be needed to understand and continue the work in subsequent messages.

Here's an example of how your output should be structured:

<example>
<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

2. Key Technical Concepts:
   - [Concept 1]
   - [Concept 2]

3. Files and Code Sections:
   - [File Name 1]
      - [Summary of why this file is important]
      - [Important Code Snippet]

4. Errors and fixes:
    - [Error description]:
      - [How you fixed it]

5. Problem Solving:
   [Description]

6. All user messages:
    - [Detailed non tool use user message]

7. Pending Tasks:
   - [Task 1]

8. Work Completed:
   [Description of what was accomplished]

9. Context for Continuing Work:
   [Key context, decisions, or state needed to continue the work]

</summary>
</example>

Please provide your summary following this structure, ensuring precision and thoroughness in your response.
`

// noToolsTrailer is appended to remind model not to use tools.
const noToolsTrailer = `

REMINDER: Do NOT call any tools. Respond with plain text only — an <analysis> block followed by a <summary> block. Tool calls will be rejected and you will fail the task.`

// GetCompactPrompt returns the full compact system prompt.
// TS aligned: getCompactPrompt() (prompt.ts:293-303)
func GetCompactPrompt(customInstructions string) string {
	prompt := noToolsPreamble + baseCompactPrompt
	if customInstructions != "" {
		prompt += fmt.Sprintf("\n\nAdditional Instructions:\n%s", customInstructions)
	}
	prompt += noToolsTrailer
	return prompt
}

// GetPartialCompactPrompt returns the partial compact prompt with preservedSegment description.
// TS aligned: getPartialCompactPrompt() (prompt.ts:274-291)
// direction: "from" (default) or "up_to"
func GetPartialCompactPrompt(customInstructions, direction string) string {
	template := partialCompactPrompt
	if direction == "up_to" {
		template = partialCompactUpToPrompt
	}
	prompt := noToolsPreamble + template
	if customInstructions != "" {
		prompt += fmt.Sprintf("\n\nAdditional Instructions:\n%s", customInstructions)
	}
	prompt += noToolsTrailer
	return prompt
}

// FormatCompactSummary formats the LLM summary output by stripping <analysis> tags
// and converting <summary> to readable headers.
// TS aligned: formatCompactSummary() (prompt.ts:311-335)
func FormatCompactSummary(rawSummary string) string {
	formatted := rawSummary

	// Strip analysis section - it's a drafting scratchpad
	analysisRe := regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
	formatted = analysisRe.ReplaceAllString(formatted, "")

	// Extract and format summary section
	summaryRe := regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	if match := summaryRe.FindStringSubmatch(formatted); match != nil {
		content := match[1]
		formatted = summaryRe.ReplaceAllString(formatted, fmt.Sprintf("Summary:\n%s", strings.TrimSpace(content)))
	}

	// Clean up extra whitespace
	spaceRe := regexp.MustCompile(`\n\n+`)
	formatted = spaceRe.ReplaceAllString(formatted, "\n\n")

	return strings.TrimSpace(formatted)
}

// GetCompactUserSummaryMessage builds the user message for LLM compact.
// TS aligned: getCompactUserSummaryMessage() (prompt.ts:337-374)
// suppressFollowUp: if true, adds instruction to continue without questions
// transcriptPath: optional path to full transcript for reference
// recentMessagesPreserved: if true, notes that recent messages are preserved
func GetCompactUserSummaryMessage(summary string, suppressFollowUp bool, transcriptPath, recentMessagesPreserved string) string {
	formattedSummary := FormatCompactSummary(summary)
	base := fmt.Sprintf(`This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.

%s`, formattedSummary)

	if transcriptPath != "" {
		base += fmt.Sprintf("\n\nIf you need specific details from before compaction (like exact code snippets, error messages, or content you generated), read the full transcript at: %s", transcriptPath)
	}

	if recentMessagesPreserved != "" {
		base += "\n\nRecent messages are preserved verbatim."
	}

	if !suppressFollowUp {
		return base
	}

	// Add continuation instruction
	continuation := fmt.Sprintf(`%s
Continue the conversation from where it left off without asking the user any further questions. Resume directly — do not acknowledge the summary, do not recap what was happening, do not preface with "I'll continue" or similar. Pick up the last task as if the break never happened.`, base)

	// Note: Go port doesn't support proactive mode (feature flags removed)
	// Original TS has additional proactive mode instruction here

	return continuation
}
