package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ─── Peak-relative drawdown guard ────────────────────────────────────────
//
// The previous capital-protection tiers keyed off ABSOLUTE dollar thresholds
// ($20 / $50 / $75). Absolute thresholds do not measure risk: an account that
// grows to $400 and falls to $80 has lost 80% of its capital while sitting in
// the "CAUTION MODE" band, and the same $80 on an account that never exceeded
// $85 is a 6% drawdown. What matters is how far equity has fallen from its own
// high-water mark, so the tiers below are expressed relative to peak equity.
//
// Peak equity is persisted to disk. The bot runs on a 15-minute cron, so a
// peak held only in memory resets to the current balance on every invocation —
// which makes the drawdown permanently 0% and the guard permanently inert.

const equityStatePath = "equity_state.json"

// Drawdown tier boundaries, as a fraction below peak equity.
const (
	DD_TIER_RISK_75   = 0.05 // −5%:  position risk cut to 75%
	DD_TIER_RISK_50   = 0.08 // −8%:  position risk cut to 50%
	DD_TIER_CONV3     = 0.10 // −10%: only Conviction 3 setups
	DD_TIER_NO_ENTRY  = 0.12 // −12%: no new trades
	DD_TIER_HARD_HALT = 0.15 // −15%: hard halt, manual review required
)

// EquityState is the persisted high-water-mark record. It survives process
// restarts so the 15-minute cron measures drawdown against the real peak
// rather than against whatever the balance happened to be this run.
type EquityState struct {
	PeakEquity float64   `json:"peak_equity"`
	PeakAt     time.Time `json:"peak_at"`
	LastEquity float64   `json:"last_equity"`
	UpdatedAt  time.Time `json:"updated_at"`

	// Halted latches once the −15% tier fires and is NOT cleared automatically.
	// A halted bot stays halted across restarts until a human clears it — that
	// is the "manual review" requirement. Clearing is done by deleting
	// equity_state.json or setting "halted": false in it.
	Halted     bool      `json:"halted"`
	HaltedAt   time.Time `json:"halted_at,omitempty"`
	HaltReason string    `json:"halt_reason,omitempty"`
}

// DrawdownAction is the guard's decision for this cycle.
type DrawdownAction struct {
	Tier            string
	PeakEquity      float64
	CurrentEquity   float64
	DrawdownPct     float64 // positive number: 7.3 means 7.3% below peak
	RiskMultiplier  float64 // scales position risk; 1.0 = unrestricted
	MinConviction   int
	BlockNewEntries bool
	HardHalt        bool
	Message         string
}

// LoadEquityState reads equity_state.json. A missing or unparseable file
// yields a zero-valued state, which callers seed from the current balance.
func LoadEquityState() *EquityState {
	var st EquityState
	data, err := os.ReadFile(equityStatePath)
	if err != nil {
		return &st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		fmt.Printf("   ⚠️ equity_state.json unreadable (%v) — starting a fresh high-water mark\n", err)
		return &EquityState{}
	}
	return &st
}

// Save writes the state back to disk. Written with a temp file + rename so a
// crash mid-write cannot leave a truncated high-water mark behind.
func (s *EquityState) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := equityStatePath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, equityStatePath)
}

// EvaluateDrawdownGuard updates the persisted high-water mark from the current
// equity and returns the restrictions that apply this cycle.
//
// In scan mode the state file is read but never written: a scan run uses a
// dummy balance when the wallet fetch fails, and letting that touch the real
// high-water mark would corrupt it.
func EvaluateDrawdownGuard(currentEquity float64, scanMode bool) DrawdownAction {
	st := LoadEquityState()
	now := time.Now().UTC()

	// A latched halt outranks everything, including a recovered balance.
	if st.Halted {
		return DrawdownAction{
			Tier:            "HALTED",
			PeakEquity:      st.PeakEquity,
			CurrentEquity:   currentEquity,
			DrawdownPct:     drawdownPct(st.PeakEquity, currentEquity),
			RiskMultiplier:  0,
			MinConviction:   99,
			BlockNewEntries: true,
			HardHalt:        true,
			Message: fmt.Sprintf(
				"HALT LATCHED %s: %s — clear equity_state.json after manual review to resume",
				st.HaltedAt.Format(time.RFC3339), st.HaltReason),
		}
	}

	// Seed or raise the high-water mark.
	if currentEquity > st.PeakEquity {
		st.PeakEquity = currentEquity
		st.PeakAt = now
	}
	st.LastEquity = currentEquity
	st.UpdatedAt = now

	dd := drawdownPct(st.PeakEquity, currentEquity)

	act := DrawdownAction{
		PeakEquity:     st.PeakEquity,
		CurrentEquity:  currentEquity,
		DrawdownPct:    dd,
		RiskMultiplier: 1.0,
		MinConviction:  2,
	}

	// Tiers are cumulative: each deeper tier keeps the restrictions above it.
	switch {
	case dd >= DD_TIER_HARD_HALT*100:
		act.Tier = "HARD_HALT"
		act.RiskMultiplier = 0
		act.MinConviction = 99
		act.BlockNewEntries = true
		act.HardHalt = true
		act.Message = fmt.Sprintf("🛑 HARD HALT: %.2f%% below peak $%.2f (limit %.0f%%). Manual review required.",
			dd, st.PeakEquity, DD_TIER_HARD_HALT*100)
		st.Halted = true
		st.HaltedAt = now
		st.HaltReason = fmt.Sprintf("drawdown %.2f%% from peak $%.2f (equity $%.2f)", dd, st.PeakEquity, currentEquity)

	case dd >= DD_TIER_NO_ENTRY*100:
		act.Tier = "NO_NEW_ENTRIES"
		act.RiskMultiplier = 0.50
		act.MinConviction = 3
		act.BlockNewEntries = true
		act.Message = fmt.Sprintf("🚫 NO NEW ENTRIES: %.2f%% below peak $%.2f. Managing open positions only.",
			dd, st.PeakEquity)

	case dd >= DD_TIER_CONV3*100:
		act.Tier = "CONVICTION_3_ONLY"
		act.RiskMultiplier = 0.50
		act.MinConviction = 3
		act.Message = fmt.Sprintf("🔴 CONVICTION 3 ONLY: %.2f%% below peak $%.2f. Risk at 50%%.",
			dd, st.PeakEquity)

	case dd >= DD_TIER_RISK_50*100:
		act.Tier = "RISK_50"
		act.RiskMultiplier = 0.50
		act.Message = fmt.Sprintf("🟠 RISK 50%%: %.2f%% below peak $%.2f.", dd, st.PeakEquity)

	case dd >= DD_TIER_RISK_75*100:
		act.Tier = "RISK_75"
		act.RiskMultiplier = 0.75
		act.Message = fmt.Sprintf("🟡 RISK 75%%: %.2f%% below peak $%.2f.", dd, st.PeakEquity)

	default:
		act.Tier = "NORMAL"
		act.Message = fmt.Sprintf("🟢 NORMAL: %.2f%% below peak $%.2f.", dd, st.PeakEquity)
	}

	if !scanMode {
		if err := st.Save(); err != nil {
			fmt.Printf("   ⚠️ Could not persist equity state: %v\n", err)
		}
	}

	return act
}

// drawdownPct returns how far below peak the current equity sits, as a
// percentage. Zero when at or above peak, or when no peak is recorded yet.
func drawdownPct(peak, current float64) float64 {
	if peak <= 0 || current >= peak {
		return 0
	}
	return (peak - current) / peak * 100.0
}

// ApplyAbsoluteFloor is the secondary safety net beneath the percentage tiers.
// The percentage system governs how much risk to take; this floor governs
// whether an account is large enough to place a compliant order at all.
// It deliberately does NOT replace the drawdown tiers — a large account in a
// deep drawdown clears this floor easily and is still restricted above.
func ApplyAbsoluteFloor(act DrawdownAction, equity float64) DrawdownAction {
	// Below the exchange minimum notional there is no order to place.
	if equity < MIN_ORDER_USD*2 {
		act.BlockNewEntries = true
		act.Message += fmt.Sprintf(" | 🚨 ABSOLUTE FLOOR: $%.2f cannot fund a $%.2f minimum order plus fees.",
			equity, MIN_ORDER_USD)
	}
	return act
}
