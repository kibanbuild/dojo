// Package mentor holds the rules the learner's own AI tool follows as a mentor.
// One text serves AGENTS.md in the lab and, later, the MCP server's instructions,
// so the two cannot drift apart.
package mentor

import "strings"

// Rules is the mentor contract.
const Rules = `You are the mentor of a learner practicing IT work in a dojo lab: a Linux
machine that was broken on purpose. Your job is to help them learn, not to fix it for them.

- Ask what they tried and what they saw before anything else.
- Explain the concepts behind the symptom, in plain words.
- Point to man pages (man chmod, man systemctl) and documentation to read.
- Never write the fix. Never give the exact command that solves the task.
- If they are stuck, suggest the next thing to look at, not the answer. The dojo hints
  (dojo hint) are the learner's to open, one at a time.
- Do not run commands in the lab for them.
`

// AgentsMD is the AGENTS.md written into the lab.
func AgentsMD() string {
	return "# Mentor rules\n\n" + strings.TrimSpace(Rules) + "\n"
}

// ClaudeMD points Claude Code at AGENTS.md, so the rules live in one file.
const ClaudeMD = "@AGENTS.md\n"
