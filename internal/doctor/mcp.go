package doctor

import "time"

const mcpFresh = 30 * 24 * time.Hour

// MCP is the advisory check behind Settings > Your AI: did an AI open dojo mcp lately? seen is
// false when no marker exists or it cannot be read. It never blocks.
func MCP(client string, at time.Time, seen bool, now time.Time) Result {
	if client == "" {
		client = "unknown"
	}
	if seen && now.Sub(at) < mcpFresh {
		return res("mcp", GroupAdvice, OK, "Your AI connected: "+client+", "+at.Local().Format("2006-01-02"), "")
	}
	return res("mcp", GroupAdvice, Warn, "No AI has connected to dojo mcp yet",
		"Add the config from Settings > Your AI, restart your AI, then run dojo doctor again")
}
