package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// KronosLogEntry captures every Kronos AI prediction alongside the bot's own
// master signal (before the Kronos overlay adjusts it), so a later analysis
// pass can join this against realized price movement and measure whether
// Kronos or the indicator stack called the move correctly.
type KronosLogEntry struct {
	Timestamp        time.Time    `json:"timestamp"`
	Symbol           string       `json:"symbol"`
	Price            float64      `json:"price"`
	MasterAction     SignalAction `json:"master_action"`
	MasterStrategy   string       `json:"master_strategy"`
	PreConviction    int          `json:"pre_conviction"`
	PreConfidence    float64      `json:"pre_confidence"`
	KronosDirection  string       `json:"kronos_direction"`
	KronosZone       string       `json:"kronos_zone"`
	KronosComposite  float64      `json:"kronos_composite"`
	KronosConfidence float64      `json:"kronos_confidence"`
	KronosPrice      float64      `json:"kronos_price"`
	Agreement        string       `json:"agreement"` // "agree" | "disagree" | "neutral"
}

const kronosLogPath = "../kronos_log.jsonl"

// AppendKronosLog writes one JSON line per Kronos prediction to kronos_log.jsonl.
// A later script can join these records (by symbol + timestamp) against
// realized price moves to measure Kronos's hit rate vs. the indicator stack,
// and against agreement/disagreement to tune the overlay's weight.
func AppendKronosLog(entry KronosLogEntry) {
	f, err := os.OpenFile(kronosLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("   ⚠️ Kronos log write failed: %v\n", err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Printf("   ⚠️ Kronos log marshal failed: %v\n", err)
		return
	}
	f.Write(append(data, '\n'))
}
