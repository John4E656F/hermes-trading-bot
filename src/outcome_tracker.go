package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// TradeOutcome is appended to trade_log.jsonl after a position closes.
// Keyed by symbol + entry_timestamp so it can be joined with TradeLogEntry.
type TradeOutcome struct {
	Type           string    `json:"type"`            // always "outcome"
	Symbol         string    `json:"symbol"`
	EntryTimestamp time.Time `json:"entry_timestamp"` // matches TradeLogEntry.Timestamp
	ExitTimestamp  time.Time `json:"exit_timestamp"`
	EntryPrice     float64   `json:"entry_price"`
	ExitPrice      float64   `json:"exit_price"`
	ClosedPnL      float64   `json:"closed_pnl"`
	ClosedPnLPct   float64   `json:"closed_pnl_pct"` // relative to entry notional
	Outcome        string    `json:"outcome"`         // "TP_HIT" | "SL_HIT" | "TRAIL_STOP" | "MANUAL" | "UNKNOWN"
	HoldHours      float64   `json:"hold_hours"`
}

// UpdateTradeOutcomes fetches recently closed positions from Bybit and appends
// outcome records to trade_log.jsonl. Run at the end of each cycle so every
// closed trade gets annotated with its actual PnL result.
func UpdateTradeOutcomes(client *BybitClient) {
	respBytes, err := client.GetPrivateRequest(
		"/v5/position/closed-pnl?category=linear&limit=50",
	)
	if err != nil {
		fmt.Printf("⚠️ Outcome tracker: closed PnL fetch failed: %v\n", err)
		return
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol         string `json:"symbol"`
				OrderType      string `json:"orderType"`
				ClosedSize     string `json:"closedSize"`
				AvgEntryPrice  string `json:"avgEntryPrice"`
				AvgExitPrice   string `json:"avgExitPrice"`
				ClosedPnl      string `json:"closedPnl"`
				CreatedTime    string `json:"createdTime"`
				UpdatedTime    string `json:"updatedTime"`
				ExecType       string `json:"execType"` // "Trade", "BustTrade", etc.
			} `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(respBytes, &resp); err != nil || resp.RetCode != 0 {
		return
	}

	if len(resp.Result.List) == 0 {
		return
	}

	// Load existing trade_log.jsonl to find matching open entries.
	// We only write an outcome record if we haven't already written one
	// for this symbol+exitTime combination.
	existing := loadExistingOutcomeKeys()

	var written int
	for _, item := range resp.Result.List {
		entryPrice, _ := strconv.ParseFloat(item.AvgEntryPrice, 64)
		exitPrice, _ := strconv.ParseFloat(item.AvgExitPrice, 64)
		closedPnL, _ := strconv.ParseFloat(item.ClosedPnl, 64)
		closedSize, _ := strconv.ParseFloat(item.ClosedSize, 64)

		exitTsMs, _ := strconv.ParseInt(item.UpdatedTime, 10, 64)
		entryTsMs, _ := strconv.ParseInt(item.CreatedTime, 10, 64)

		exitTime := time.UnixMilli(exitTsMs).UTC()
		entryTime := time.UnixMilli(entryTsMs).UTC()

		// Dedup key: symbol + exit timestamp
		key := item.Symbol + "_" + item.UpdatedTime
		if existing[key] {
			continue
		}

		// Classify outcome
		outcome := classifyOutcome(item.OrderType, item.ExecType, closedPnL)

		// PnL as % of notional
		pnlPct := 0.0
		if entryPrice > 0 && closedSize > 0 {
			notional := entryPrice * closedSize
			if notional > 0 {
				pnlPct = (closedPnL / notional) * 100.0
			}
		}

		holdHours := exitTime.Sub(entryTime).Hours()

		record := TradeOutcome{
			Type:           "outcome",
			Symbol:         item.Symbol,
			EntryTimestamp: entryTime,
			ExitTimestamp:  exitTime,
			EntryPrice:     entryPrice,
			ExitPrice:      exitPrice,
			ClosedPnL:      closedPnL,
			ClosedPnLPct:   pnlPct,
			Outcome:        outcome,
			HoldHours:      holdHours,
		}

		AppendTradeLog(TradeLogEntry{
			Timestamp:  record.ExitTimestamp,
			Symbol:     record.Symbol,
			SkipReason: fmt.Sprintf("OUTCOME:%s pnl=$%.4f (%.2f%%) hold=%.1fh entry=$%.4f exit=$%.4f",
				outcome, closedPnL, pnlPct, holdHours, entryPrice, exitPrice),
			Executed: outcome == "TP_HIT",
		})

		// Also write as a proper outcome JSON line for structured analysis
		appendOutcomeRecord(record)
		written++

		sign := "+"
		if closedPnL < 0 {
			sign = ""
		}
		fmt.Printf("   📊 OUTCOME [%s] %s: %s$%.4f (%.2f%%) — %s after %.1fh\n",
			outcome, item.Symbol, sign, closedPnL, pnlPct, outcome, holdHours)
	}

	if written > 0 {
		fmt.Printf("   📝 %d outcome(s) recorded to trade log.\n", written)
	}
}

func classifyOutcome(orderType, execType string, pnl float64) string {
	switch orderType {
	case "TakeProfit":
		return "TP_HIT"
	case "StopLoss":
		return "SL_HIT"
	case "TrailingStop":
		return "TRAIL_STOP"
	case "Market", "Limit":
		if pnl > 0 {
			return "MANUAL_WIN"
		}
		return "MANUAL_LOSS"
	}
	if execType == "BustTrade" {
		return "LIQUIDATION"
	}
	return "UNKNOWN"
}

// appendOutcomeRecord writes a structured outcome record to outcome_log.jsonl.
// This is separate from trade_log.jsonl so analysis scripts can load just outcomes.
func appendOutcomeRecord(record TradeOutcome) {
	const outcomePath = "../outcome_log.jsonl"
	f, err := os.OpenFile(outcomePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	f.Write(append(data, '\n'))
}

// loadExistingOutcomeKeys reads outcome_log.jsonl and returns a set of
// symbol_timestamp keys already recorded (prevents duplicate outcome entries).
func loadExistingOutcomeKeys() map[string]bool {
	keys := make(map[string]bool)
	data, err := os.ReadFile("../outcome_log.jsonl")
	if err != nil {
		return keys
	}
	for _, line := range splitLines(data) {
		var rec TradeOutcome
		if json.Unmarshal(line, &rec) == nil {
			key := rec.Symbol + "_" + strconv.FormatInt(rec.ExitTimestamp.UnixMilli(), 10)
			keys[key] = true
		}
	}
	return keys
}

// splitLines splits a byte slice by newlines, skipping empty lines.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
