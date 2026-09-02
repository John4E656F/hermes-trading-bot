package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
)

// ReflectionSummary captures what a symbol's trading history tells us,
// split by side (BUY vs SELL) so we don't dilute a good BUY record with
// a bad SELL record or vice versa.
type ReflectionSummary struct {
	Symbol               string  `json:"symbol"`
	Side                 string  `json:"side"` // "BUY" or "SELL" — added 2026-09-02 for side-split
	TotalCalls           int     `json:"total_calls"`
	CorrectCalls         int     `json:"correct_calls"`
	WinRate              float64 `json:"win_rate"`
	RecentCalls          int     `json:"recent_calls"`
	RecentCorrect        int     `json:"recent_correct"`
	RecentWinRate        float64 `json:"recent_win_rate"`
	AvgWinPct            float64 `json:"avg_win_pct"`
	AvgLossPct           float64 `json:"avg_loss_pct"`
	TotalPnlPct          float64 `json:"total_pnl_pct"`
	Trend                string  `json:"trend"`
	Lesson               string  `json:"lesson"`
	ConfidenceMultiplier float64 `json:"confidence_multiplier"`
}

var (
	reflectionCache     map[string]ReflectionSummary
	reflectionCacheOnce sync.Once
)

func ComputeReflections() map[string]ReflectionSummary {
	reflectionCacheOnce.Do(func() {
		reflectionCache = computeReflectionsUnsafe()
	})
	return reflectionCache
}

func InvalidateReflectionCache() {
	reflectionCache = nil
	reflectionCacheOnce = sync.Once{}
}

func computeReflectionsUnsafe() map[string]ReflectionSummary {
	outcomes := readKronosOutcomes()
	if len(outcomes) == 0 {
		fmt.Println("   📖 Reflection: no outcomes data yet — skipping.")
		return nil
	}

	type callRecord struct {
		ChangePct float64
		Correct   bool
	}
	// Group by symbol + side so BUY and SELL performance is tracked independently.
	// This prevents a strong BUY record from being diluted by weak SELLs.
	grouped := make(map[string][]callRecord) // key = "SYMBOL_SIDE"
	for _, o := range outcomes {
		if o.MasterResult == "no_call" || o.MasterAction == ACTION_HOLD || o.MasterAction == "" {
			continue
		}
		correct := o.MasterResult == "correct"
		key := string(o.Symbol) + "_" + string(o.MasterAction)
		grouped[key] = append(grouped[key], callRecord{
			ChangePct: o.ChangePct,
			Correct:   correct,
		})
	}

	result := make(map[string]ReflectionSummary, len(grouped))
	for key, calls := range grouped {
		// Parse symbol and side from the composite key
		symSide := strings.Split(key, "_")
		if len(symSide) < 2 {
			continue
		}
		sym := symSide[0]
		side := symSide[1]
		total := len(calls)
		correct := 0
		for _, c := range calls {
			if c.Correct {
				correct++
			}
		}
		winRate := float64(correct) / float64(total)

		recentN := 20
		if recentN > total {
			recentN = total
		}
		recentCalls := calls[total-recentN:]
		recentCorrect := 0
		for _, c := range recentCalls {
			if c.Correct {
				recentCorrect++
			}
		}
		recentWR := float64(recentCorrect) / float64(recentN)

		var totalWinPct, totalLossPct float64
		var winCount, lossCount int
		var totalPnl float64
		for _, c := range calls {
			totalPnl += c.ChangePct
			if c.Correct {
				totalWinPct += math.Abs(c.ChangePct)
				winCount++
			} else {
				totalLossPct += math.Abs(c.ChangePct)
				lossCount++
			}
		}

		avgWin := 0.0
		if winCount > 0 {
			avgWin = totalWinPct / float64(winCount)
		}
		avgLoss := 0.0
		if lossCount > 0 {
			avgLoss = totalLossPct / float64(lossCount)
		}

		trend := "stable"
		if total >= 30 {
			firstHalf := total / 2
			firstWR := 0.0
			if firstHalf > 0 {
				firstCorrect := 0
				for i := 0; i < firstHalf; i++ {
					if calls[i].Correct {
						firstCorrect++
					}
				}
				firstWR = float64(firstCorrect) / float64(firstHalf)
			}
			if recentWR > firstWR+0.1 {
				trend = "improving"
			} else if recentWR < firstWR-0.1 {
				trend = "declining"
			}
		}

		multiplier := 1.0
		switch {
		case winRate >= 0.65:
			multiplier = 1.2
		case winRate >= 0.55:
			multiplier = 1.1
		case winRate >= 0.45:
			multiplier = 1.0
		case winRate >= 0.35:
			multiplier = 0.9
		default:
			multiplier = 0.75
		}
		if trend == "improving" && winRate < 0.50 {
			multiplier += 0.1
		} else if trend == "declining" && winRate >= 0.50 {
			multiplier -= 0.1
		}
		if multiplier < 0.5 {
			multiplier = 0.5
		}
		if multiplier > 1.5 {
			multiplier = 1.5
		}

		lesson := buildLesson(sym, winRate, recentWR, total, trend, avgWin, avgLoss)

		sideKey := sym + "_" + side
		result[sideKey] = ReflectionSummary{
			Symbol:               sym,
			Side:                 side,
			TotalCalls:           total,
			CorrectCalls:         correct,
			WinRate:              winRate,
			RecentCalls:          recentN,
			RecentCorrect:        recentCorrect,
			RecentWinRate:        recentWR,
			AvgWinPct:            avgWin,
			AvgLossPct:           avgLoss,
			TotalPnlPct:          totalPnl,
			Trend:                trend,
			Lesson:               lesson,
			ConfidenceMultiplier: multiplier,
		}
	}

	fmt.Printf("   📖 Reflection: computed %d symbol profiles from %d outcomes.\n", len(result), len(outcomes))
	return result
}

func buildLesson(sym string, wr, recentWR float64, total int, trend string, avgWin, avgLoss float64) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%s win rate: %.0f%% (%d calls)", sym, wr*100, total))
	if recentWR != wr {
		parts = append(parts, fmt.Sprintf("recent: %.0f%%", recentWR*100))
	}
	parts = append(parts, trend)
	if avgWin > 0 && avgLoss > 0 {
		ratio := avgWin / avgLoss
		if ratio > 2.0 {
			parts = append(parts, "winners 2x+ larger than losers ✓")
		} else if ratio < 0.5 {
			parts = append(parts, "losers larger than winners — tighten stops")
		}
	}
	if wr < 0.35 {
		parts = append(parts, "⚠️ low conviction — require stronger confirmation")
	} else if wr > 0.60 {
		parts = append(parts, "✅ reliable — prioritize this symbol")
	}
	return strings.Join(parts, " | ")
}

func GetReflection(symbol string, side SignalAction) *ReflectionSummary {
	refs := ComputeReflections()
	if refs == nil {
		return nil
	}
	// Side-split key: "SYMBOL_BUY" or "SYMBOL_SELL"
	key := symbol + "_" + string(side)
	r, ok := refs[key]
	if !ok {
		return nil
	}
	return &r
}

// IsBannedSymbol returns true if this symbol has proven unprofitable across ALL sides.
// Ban criteria: ≥10 total calls AND combined win rate < 30%.
// Hardcoded bans for historically confirmed losers.
func IsBannedSymbol(symbol string) bool {
	hardBan := map[string]bool{
		"AAVEUSDT": true,
		"IPUSDT":   true,
		"ARBUSDT":  true,
		"UNIUSDT":  true,
	}
	if hardBan[symbol] {
		return true
	}
	// Dynamic ban from reflection data (any side that clears the threshold)
	refs := ComputeReflections()
	if refs == nil {
		return false
	}
	totalCalls := 0
	correctCalls := 0
	for key, r := range refs {
		// Match keys like "AAVEUSDT_BUY" or "AAVEUSDT_SELL"
		if len(key) >= len(symbol)+1 && key[:len(symbol)] == symbol && key[len(symbol)] == '_' {
			totalCalls += r.TotalCalls
			correctCalls += r.CorrectCalls
		}
	}
	if totalCalls >= 10 {
		wr := float64(correctCalls) / float64(totalCalls)
		if wr < 0.30 {
			return true
		}
	}
	return false
}

// IsWhitelistedSymbol returns true if this symbol has proven consistently profitable.
// Whitelist criteria: ≥10 total calls AND combined win rate > 65%.
func IsWhitelistedSymbol(symbol string) bool {
	refs := ComputeReflections()
	if refs == nil {
		return false
	}
	totalCalls := 0
	correctCalls := 0
	for key, r := range refs {
		if len(key) >= len(symbol)+1 && key[:len(symbol)] == symbol && key[len(symbol)] == '_' {
			totalCalls += r.TotalCalls
			correctCalls += r.CorrectCalls
		}
	}
	if totalCalls >= 10 {
		wr := float64(correctCalls) / float64(totalCalls)
		if wr > 0.65 {
			return true
		}
	}
	return false
}

type kronosOutcomeLine struct {
	Symbol       string       `json:"symbol"`
	ChangePct    float64      `json:"change_pct"`
	MasterResult string       `json:"master_result"`
	MasterAction SignalAction `json:"master_action"`
}

func readKronosOutcomes() []kronosOutcomeLine {
	f, err := os.Open(kronosOutcomePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var outcomes []kronosOutcomeLine
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec kronosOutcomeLine
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Symbol == "" {
			continue
		}
		outcomes = append(outcomes, rec)
	}
	return outcomes
}

func ListReflections() string {
	refs := ComputeReflections()
	if refs == nil {
		return "No reflection data available yet."
	}

	type entry struct {
		sym string
		ref ReflectionSummary
	}
	list := make([]entry, 0, len(refs))
	for _, ref := range refs {
		list = append(list, entry{sym: ref.Symbol + " " + ref.Side, ref: ref})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].ref.WinRate != list[j].ref.WinRate {
			return list[i].ref.WinRate < list[j].ref.WinRate
		}
		return list[i].ref.Side < list[j].ref.Side
	})

	var b strings.Builder
	b.WriteString("📖 Reflection — Per-Symbol Per-Side Master Signal History\n\n")
	b.WriteString(fmt.Sprintf("%-12s %6s %8s %6s %5s\n", "Symbol", "Calls", "WinRate", "Trend", "Mult"))
	b.WriteString(strings.Repeat("─", 50) + "\n")
	for _, e := range list {
		trendSym := "→"
		if e.ref.Trend == "improving" {
			trendSym = "↑"
		} else if e.ref.Trend == "declining" {
			trendSym = "↓"
		}
		b.WriteString(fmt.Sprintf("%-12s %6d %7.0f%% %6s %5.1fx\n",
			e.sym, e.ref.TotalCalls, e.ref.WinRate*100, trendSym, e.ref.ConfidenceMultiplier))
	}
	return b.String()
}