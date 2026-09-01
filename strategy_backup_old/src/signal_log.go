package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// SignalSnapshotEntry captures every non-HOLD signal the bot saw this cycle —
// whether it was executed or skipped, and why. Joining this against later
// price action (via analyze_signals.py) answers: are the gates avoiding real
// losses, or filtering out trades that would have been profitable?
type SignalSnapshotEntry struct {
	Timestamp  time.Time    `json:"timestamp"`
	Symbol     string       `json:"symbol"`
	Price      float64      `json:"price"`
	Action     SignalAction `json:"action"`
	Strategy   string       `json:"strategy"`
	Conviction int          `json:"conviction"`
	Confidence float64      `json:"confidence"`
	Gain7D     float64      `json:"gain_7d"`
	Executed   bool         `json:"executed"`
	SkipReason string       `json:"skip_reason,omitempty"`
}

const signalLogPath = "signal_log.jsonl"

// AppendSignalSnapshot writes one JSON line per evaluated signal to
// signal_log.jsonl. Run analyze_signals.py later to compare each entry's
// price at evaluation time against current price, broken out by
// executed vs. skip_reason — this shows whether skipped signals would have
// been winners (missed profit) or losers (gates working as intended).
func AppendSignalSnapshot(entry SignalSnapshotEntry) {
	f, err := os.OpenFile(signalLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("   ⚠️ Signal log write failed: %v\n", err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Printf("   ⚠️ Signal log marshal failed: %v\n", err)
		return
	}
	f.Write(append(data, '\n'))
}
