package main

import (
	"encoding/json"
	"fmt"
)

// UpdateTradeOutcomes fetches recently closed positions from Bybit and appends
// one normalized OutcomeRecord per COMPLETED trade to outcome_log.jsonl.
//
// outcome_log.jsonl is the statistics source of record. trade_log.jsonl stays
// an append-only audit trail of every decision (including rejections and
// skips); it is not a trade list and must not be counted as one.
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
			List []closedPnLItem `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(respBytes, &resp); err != nil {
		fmt.Printf("⚠️ Outcome tracker: parse failed: %v\n", err)
		return
	}
	if resp.RetCode != 0 {
		fmt.Printf("⚠️ Outcome tracker: bybit error [%d]: %s\n", resp.RetCode, resp.RetMsg)
		return
	}
	if len(resp.Result.List) == 0 {
		fmt.Println("   📊 Outcome tracker: no closed positions returned.")
		return
	}

	recorded := LoadRecordedTradeIDs()
	entries := LoadTradeLogEntries()

	written, skipped, incomplete := 0, 0, 0
	for _, item := range resp.Result.List {
		rec := buildOutcomeRecord(client, item, entries)
		if recorded[rec.TradeID] {
			skipped++
			continue
		}
		if err := AppendOutcomeRecord(rec); err != nil {
			fmt.Printf("   ⚠️ Outcome write failed for %s: %v\n", rec.TradeID, err)
			continue
		}
		recorded[rec.TradeID] = true
		written++
		if len(rec.Incomplete) > 0 {
			incomplete++
		}

		rStr := "n/a"
		if rec.ResultR != nil {
			rStr = fmt.Sprintf("%+.2fR", *rec.ResultR)
		}
		fmt.Printf("   📊 OUTCOME [%s] %s %s: net $%+.4f (%s) fees $%.4f — %s\n",
			rec.ExitReason, rec.Side, rec.Symbol, rec.NetPnL, rStr, rec.Fees, rec.StrategyPrimary)
		if len(rec.Incomplete) > 0 {
			fmt.Printf("      ⚠️ incomplete fields: %v\n", rec.Incomplete)
		}
	}

	fmt.Printf("   📝 Outcome tracker: %d written, %d already recorded, %d with incomplete fields → %s\n",
		written, skipped, incomplete, outcomeLogPath)
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
