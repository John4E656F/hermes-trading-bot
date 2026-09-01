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

// ReflectionSummary captures what a symbol's trading history tells us.
// Computed once per cycle before the signal loop.
type ReflectionSummary struct {
	Symbol string `json:"symbol"`

	// Overall stats
	TotalCalls  int     `json:"total_calls"`
	CorrectCalls int    `json:"correct_calls"`
	WinRate     float64 `json:"win_rate"` // 0.0–1.0

	// Recent performance (last 20 calls)
	RecentCalls   int     `json:"recent_calls"`
	RecentCorrect int     `json:"recent_correct"`
	RecentWinRate float64 `json:"recent_win_rate"`

	// Magnitude — how much did wins/losses move?
	AvgWinPct  float64 `json:"avg_win_pct"`
	AvgLossPct float64 `json:"avg_loss_pct"`
	TotalPnlPct float64 `json:"total_pnl_pct"` // sum of all change_pct

	// Trend — is it getting better or worse?
	Trend string `json:"trend"` // "improving" | "declining" | "stable"

	// Human-readable lesson
	Lesson string `json:"lesson"`

	// Confidence multiplier (0.5–1.5) applied to raw signal confidence
	ConfidenceMultiplier float64 `json:"confidence_multiplier"`
}

// reflectionCache is computed once per cycle and reused across all assets.
var (
	reflectionCache     map[string]ReflectionSummary
	reflectionCacheOnce sync.Once
)

// ComputeReflections reads kronos_outcomes.jsonl and produces per-symbol
// reflection summaries. Call once per cycle before the signal evaluation loop.
// Safe for concurrent reads after the first call.
func ComputeReflections() map[string]ReflectionSummary {
	reflectionCacheOnce.Do(func() {
		reflectionCache = computeReflectionsUnsafe()
	})
	return reflectionCache
}

// InvalidateReflectionCache forces a re-read on the next call. Call after the
// cron cycle's outcome resolution step so fresh outcomes are reflected.
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

	// Group by symbol
	type callRecord struct {
		ChangePct float64
		Correct   bool // master_result == "correct"
	}
	grouped := make(map[string][]callRecord)
	for _, o := range outcomes {
		if o.MasterResult == "no_call" {
			continue
		}
		correct := o.MasterResult == "correct"
		grouped[o.Symbol] = append(grouped[o.Symbol], callRecord{
			ChangePct: o.ChangePct,
			Correct:   correct,
		})
	}

	result := make(map[string]ReflectionSummary, len(grouped))
	for sym, calls := range grouped {
		total := len(calls)
		correct := 0
		for _, c := range calls {
			if c.Correct {
				correct++
			}
		}
		winRate := float64(correct) / float64(total)

		// Recent performance (last 20)
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

		// Magnitude
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

		// Trend
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

		// Confidence multiplier
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
		// Trend boost/penalty
		if trend == "improving" && winRate < 0.50 {
			multiplier += 0.1 // give it another chance
		} else if trend == "declining" && winRate >= 0.50 {
			multiplier -= 0.1 // losing edge
		}
		// Clamp
		if multiplier < 0.5 {
			multiplier = 0.5
		}
		if multiplier > 1.5 {
			multiplier = 1.5
		}

		// Build lesson
		lesson := buildLesson(sym, winRate, recentWR, total, trend, avgWin, avgLoss)

		result[sym] = ReflectionSummary{
			Symbol:               sym,
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

	// Advice
	if wr < 0.35 {
		parts = append(parts, "⚠️ low conviction — require stronger confirmation")
	} else if wr > 0.60 {
		parts = append(parts, "✅ reliable — prioritize this symbol")
	}

	return strings.Join(parts, " | ")
}

// ReadKronosOutcomesForSymbol is a convenience accessor used during signal eval.
func GetReflection(symbol string) *ReflectionSummary {
	refs := ComputeReflections()
	if refs == nil {
		return nil
	}
	r, ok := refs[symbol]
	if !ok {
		return nil
	}
	return &r
}

// ── File I/O ───────────────────────────────────────────────────────────────

type kronosOutcomeLine struct {
	Symbol       string  `json:"symbol"`
	ChangePct    float64 `json:"change_pct"`
	MasterResult string  `json:"master_result"`
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

// ── Per-Strategy Reflection (optional enhancement) ────────────────────────
// Strategy-level analysis needs signal_log.jsonl which has strategy names.
// We don't have that joined with change_pct, so for now we do per-symbol
// reflection. Strategy auto-tuning is added separately.

// ListReflections prints a sorted summary of all reflections (for --mode=scan output).
func ListReflections() string {
	refs := ComputeReflections()
	if refs == nil {
		return "No reflection data available yet."
	}

	type entry struct {
		sym  string
		ref  ReflectionSummary
	}
	list := make([]entry, 0, len(refs))
	for sym, ref := range refs {
		list = append(list, entry{sym, ref})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ref.WinRate < list[j].ref.WinRate
	})

	var b strings.Builder
	b.WriteString("📖 Reflection — Per-Symbol Master Signal History\n\n")
	b.WriteString(fmt.Sprintf("%-12s %6s %8s %6s %6s\n", "Symbol", "Calls", "WinRate", "Trend", "Mult"))
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