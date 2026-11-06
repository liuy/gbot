//go:build race

package llm

func init() { raceEnabled = true }
