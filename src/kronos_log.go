package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

// kronosResolveHorizon is the fixed lookahead used to score predictions:
// 24h after a prediction is logged, we check where price actually went and
// record whether Kronos and/or the master signal called the direction right.
const kronosResolveHorizon = 24 * time.Hour

// KronosOutcomeEntry records the result of a Kronos prediction (and the
// master signal it was compared against) 24h after it was logged.
type KronosOutcomeEntry struct {
	Timestamp       time.Time    `json:"timestamp"`   // original prediction time
	ResolvedAt      time.Time    `json:"resolved_at"` // ~24h later
	Symbol          string       `json:"symbol"`
	EntryPrice      float64      `json:"entry_price"`
	ExitPrice       float64      `json:"exit_price"`
	ChangePct       float64      `json:"change_pct"`
	MasterAction    SignalAction `json:"master_action"`
	KronosDirection string       `json:"kronos_direction"`
	Agreement       string       `json:"agreement"`
	MasterResult    string       `json:"master_result"` // "correct" | "incorrect" | "no_call"
	KronosResult    string       `json:"kronos_result"` // "correct" | "incorrect" | "no_call"
}

const kronosOutcomePath = "../kronos_outcomes.jsonl"

// directionResult classifies whether `action` (a SignalAction or Kronos
// direction string) correctly called the sign of changePct.
func directionResult(action string, changePct float64) string {
	switch strings.ToLower(action) {
	case "buy", "long":
		if changePct > 0 {
			return "correct"
		}
		return "incorrect"
	case "sell", "short":
		if changePct < 0 {
			return "correct"
		}
		return "incorrect"
	default:
		return "no_call"
	}
}

// ResolveKronosOutcomes reads kronos_log.jsonl, finds predictions logged at
// least 24h ago that haven't been resolved yet, and (using the current price
// from `prices`) records whether Kronos and the master signal correctly
// called the 24h direction. Run once per cycle.
func ResolveKronosOutcomes(prices map[string]float64) {
	data, err := os.ReadFile(kronosLogPath)
	if err != nil {
		return
	}

	resolved := loadResolvedKronosKeys()
	now := time.Now().UTC()

	var written int
	for _, line := range splitLines(data) {
		var entry KronosLogEntry
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if now.Sub(entry.Timestamp) < kronosResolveHorizon {
			continue
		}
		key := entry.Symbol + "_" + entry.Timestamp.UTC().Format(time.RFC3339)
		if resolved[key] {
			continue
		}
		exitPrice, ok := prices[entry.Symbol]
		if !ok || entry.Price <= 0 {
			continue
		}

		changePct := (exitPrice - entry.Price) / entry.Price * 100.0

		outcome := KronosOutcomeEntry{
			Timestamp:       entry.Timestamp,
			ResolvedAt:      now,
			Symbol:          entry.Symbol,
			EntryPrice:      entry.Price,
			ExitPrice:       exitPrice,
			ChangePct:       changePct,
			MasterAction:    entry.MasterAction,
			KronosDirection: entry.KronosDirection,
			Agreement:       entry.Agreement,
			MasterResult:    directionResult(string(entry.MasterAction), changePct),
			KronosResult:    directionResult(entry.KronosDirection, changePct),
		}
		appendKronosOutcome(outcome)
		written++
	}

	if written > 0 {
		fmt.Printf("   📊 Resolved %d Kronos prediction(s) at 24h horizon → %s\n", written, kronosOutcomePath)
	}
}

func appendKronosOutcome(entry KronosOutcomeEntry) {
	f, err := os.OpenFile(kronosOutcomePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("   ⚠️ Kronos outcome log write failed: %v\n", err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f.Write(append(data, '\n'))
}

// loadResolvedKronosKeys reads kronos_outcomes.jsonl and returns the set of
// symbol_timestamp keys already resolved, to avoid duplicate resolution.
func loadResolvedKronosKeys() map[string]bool {
	keys := make(map[string]bool)
	data, err := os.ReadFile(kronosOutcomePath)
	if err != nil {
		return keys
	}
	for _, line := range splitLines(data) {
		var rec KronosOutcomeEntry
		if json.Unmarshal(line, &rec) == nil {
			keys[rec.Symbol+"_"+rec.Timestamp.UTC().Format(time.RFC3339)] = true
		}
	}
	return keys
}
