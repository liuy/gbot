package llm

// raceEnabled is set by the race build tag; timing-sensitive raw-TCP tests
// skip their retry-count assertions under the race detector's scheduling.
var raceEnabled = false
