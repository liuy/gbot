package tui

import (
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// StoreMessagesToEngine converts short-term store messages to engine messages.
// Delegates to short.StoreMessagesToEngine after pointer conversion.
func StoreMessagesToEngine(storeMsgs []short.TranscriptMessage) ([]types.Message, error) {
	ptrs := make([]*short.TranscriptMessage, len(storeMsgs))
	for i := range storeMsgs {
		ptrs[i] = &storeMsgs[i]
	}
	return short.StoreMessagesToEngine(ptrs)
}

// EngineMessagesToStore converts engine messages to short-term store messages.
// Delegates to short.EngineMessagesToStore and converts back to value slice
// for backward compatibility with existing callers.
func EngineMessagesToStore(engineMsgs []types.Message) ([]short.TranscriptMessage, error) {
	ptrs, err := short.EngineMessagesToStore(engineMsgs)
	if err != nil {
		return nil, err
	}
	if len(ptrs) == 0 {
		return nil, nil
	}
	vals := make([]short.TranscriptMessage, len(ptrs))
	for i, p := range ptrs {
		vals[i] = *p
	}
	return vals, nil
}
