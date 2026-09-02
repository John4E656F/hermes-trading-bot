package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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

	// ── Per-strategy lens (added for episode-deduplicated accuracy) ──
	// Strategy above is the MASTER label only. S1/S2/S3 are supporting
	// signals that never generate a master label on their own (see
	// allocator.go), so their individual accuracy was not measurable from
	// this log at all. Recording which sub-strategies fired, and in which
	// direction, makes each lens measurable independently.
	ActiveStrategies []string `json:"active_strategies,omitempty"`
	S1Action         string   `json:"s1_action,omitempty"`
	S2Action         string   `json:"s2_action,omitempty"`
	S3Action         string   `json:"s3_action,omitempty"`
	S4Action         string   `json:"s4_action,omitempty"`
	S5Action         string   `json:"s5_action,omitempty"`
	KronosDirection  string   `json:"kronos_direction,omitempty"`
	KronosConfidence float64  `json:"kronos_confidence,omitempty"`
	CouncilVerdict   string   `json:"council_verdict,omitempty"`
	Regime           string   `json:"regime,omitempty"`
}

// subStrategyLens fills the per-strategy fields from a signal. Each S-lens is
// recorded whether or not it agreed with the master action, so accuracy can be
// measured per lens rather than only for the blend.
func subStrategyLens(sig StrategySignal) (active []string, s1, s2, s3, s4, s5 string) {
	act := func(on bool, a SignalAction) string {
		if !on {
			return ""
		}
		return string(a)
	}
	s1 = act(sig.S1.Active, sig.S1.Action)
	s2 = act(sig.S2.Active, sig.S2.Action)
	s3 = act(sig.S3.Active, sig.S3.Action)
	s4 = act(sig.S4.Active, sig.S4.Action)
	s5 = act(sig.S5.Active, sig.S5.Action)
	for name, a := range map[string]string{"S1": s1, "S2": s2, "S3": s3, "S4": s4, "S5": s5} {
		if a != "" {
			active = append(active, name)
		}
	}
	sort.Strings(active)
	return
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
