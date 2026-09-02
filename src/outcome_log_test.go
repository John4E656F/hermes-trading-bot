package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// A real-shaped /v5/position/closed-pnl payload. Field names and string-typed
// numbers match Bybit's V5 response exactly.
const closedPnLFixture = `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "list": [
      {
        "symbol": "NEARUSDT",
        "orderId": "5f1a2b3c-0001",
        "side": "Sell",
        "orderType": "TakeProfit",
        "execType": "Trade",
        "closedSize": "5.9",
        "cumEntryValue": "14.368",
        "avgEntryPrice": "2.4353",
        "cumExitValue": "16.108",
        "avgExitPrice": "2.7302",
        "closedPnl": "1.7231",
        "openFee": "0.0079",
        "closeFee": "0.0089",
        "leverage": "3",
        "createdTime": "1780576026000",
        "updatedTime": "1780655226000"
      },
      {
        "symbol": "ICPUSDT",
        "orderId": "5f1a2b3c-0002",
        "side": "Sell",
        "orderType": "StopLoss",
        "execType": "Trade",
        "closedSize": "4.2",
        "cumEntryValue": "11.655",
        "avgEntryPrice": "2.775",
        "cumExitValue": "9.689",
        "avgExitPrice": "2.3070",
        "closedPnl": "-1.9788",
        "openFee": "0.0064",
        "closeFee": "0.0053",
        "leverage": "3",
        "createdTime": "1780595392000",
        "updatedTime": "1780638592000"
      }
    ]
  }
}`

func TestBuildOutcomeRecordFromBybitShape(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	// Entry context as the executor now writes it.
	entries := []TradeLogEntry{
		{
			Timestamp:            time.UnixMilli(1780576026000).UTC(),
			Symbol:               "NEARUSDT",
			Side:                 "BUY",
			EntryPrice:           2.43526,
			Conviction:           3,
			Confidence:           0.80,
			RiskPct:              0.0075,
			WalletBal:            103.69,
			Strategy:             "META: S4 Funding Contrarian",
			Regime:               "RANGING",
			S4Active:             true,
			ConfirmingStrategies: []string{"S1"},
			AIVerdict:            "PASS",
			CouncilVotes:         []string{"deepseek:CONFIRM", "claude:CONFIRM", "gemini:REJECT"},
			KronosDirection:      "buy",
			KronosConfidence:     0.61,
			Executed:             true,
		},
		{
			Timestamp:  time.UnixMilli(1780595392000).UTC(),
			Symbol:     "ICPUSDT",
			Side:       "BUY",
			EntryPrice: 2.775,
			RiskPct:    0.0050,
			WalletBal:  98.67,
			Strategy:   "S4 Funding Contrarian",
			Regime:     "RANGING",
			AIVerdict:  "PASS",
			Executed:   true,
		},
	}

	var resp struct {
		Result struct {
			List []closedPnLItem `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(closedPnLFixture), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Result.List) != 2 {
		t.Fatalf("fixture parsed %d rows, want 2", len(resp.Result.List))
	}

	for _, item := range resp.Result.List {
		// client == nil exercises the offline path: network enrichment is
		// skipped and flagged rather than silently zero-filled.
		rec := buildOutcomeRecord(nil, item, entries)
		if err := AppendOutcomeRecord(rec); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	recs, bad := LoadOutcomeRecords()
	if bad != 0 || len(recs) != 2 {
		t.Fatalf("loaded %d records (%d malformed), want 2", len(recs), bad)
	}

	win := recs[0]
	if win.Symbol != "NEARUSDT" || win.Side != "BUY" {
		t.Errorf("side derivation wrong: %s %s (closing side Sell => position BUY)", win.Symbol, win.Side)
	}
	if win.StrategyPrimary != "META: S4 Funding Contrarian" {
		t.Errorf("strategy_primary = %q", win.StrategyPrimary)
	}
	if len(win.StrategiesConfirming) != 2 {
		t.Errorf("strategies_confirming = %v, want S1+S4", win.StrategiesConfirming)
	}
	// closedPnl is net of fees; gross adds them back.
	if wantGross := 1.7231 + 0.0079 + 0.0089; !approx(win.GrossPnL, wantGross, 1e-9) {
		t.Errorf("gross_pnl = %.6f, want %.6f", win.GrossPnL, wantGross)
	}
	if !approx(win.Fees, 0.0168, 1e-9) {
		t.Errorf("fees = %.6f, want 0.0168", win.Fees)
	}
	// initial risk = wallet 103.69 * riskPct 0.0075 = 0.777675
	if win.InitialRiskUSD == nil || !approx(*win.InitialRiskUSD, 0.777675, 1e-6) {
		t.Fatalf("initial_risk_usd = %v, want 0.777675", win.InitialRiskUSD)
	}
	if win.ResultR == nil || !approx(*win.ResultR, 1.7231/0.777675, 1e-6) {
		t.Fatalf("result_r = %v, want %.4f", win.ResultR, 1.7231/0.777675)
	}
	if win.CouncilVerdict != "PASS" || len(win.CouncilVotes) != 3 {
		t.Errorf("council not carried: %q %v", win.CouncilVerdict, win.CouncilVotes)
	}
	if win.KronosDirection != "buy" || win.KronosConfidence == nil {
		t.Errorf("kronos not carried: %q %v", win.KronosDirection, win.KronosConfidence)
	}
	if !win.WouldTradeWithoutAI {
		t.Error("would_trade_without_ai should be true — every non-AI gate passed")
	}
	if win.ExitReason != "TP_HIT" {
		t.Errorf("exit_reason = %q, want TP_HIT", win.ExitReason)
	}
	if win.EquityBefore == nil || !approx(*win.EquityBefore, 103.69, 1e-9) {
		t.Errorf("equity_before = %v", win.EquityBefore)
	}
	if win.EquityAfter == nil || !approx(*win.EquityAfter, 103.69+1.7231, 1e-9) {
		t.Errorf("equity_after = %v", win.EquityAfter)
	}

	// Unmeasured fields must be null and flagged, never zero.
	if win.FundingCost != nil || win.MaxAdverseExcursion != nil {
		t.Error("offline enrichment should leave funding/MAE null, not zero")
	}
	if !contains(win.Incomplete, "no_client_no_funding") {
		t.Errorf("incomplete = %v, want it to name the missing enrichment", win.Incomplete)
	}

	loss := recs[1]
	if loss.ExitReason != "SL_HIT" || loss.NetPnL >= 0 {
		t.Errorf("loss row wrong: %s %.4f", loss.ExitReason, loss.NetPnL)
	}
	if loss.ResultR == nil || *loss.ResultR >= 0 {
		t.Errorf("loss result_r = %v, want negative", loss.ResultR)
	}
}

// A re-run must not duplicate rows that are already recorded.
func TestOutcomeDedupByTradeID(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	var resp struct {
		Result struct {
			List []closedPnLItem `json:"list"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(closedPnLFixture), &resp)

	for pass := 0; pass < 3; pass++ {
		recorded := LoadRecordedTradeIDs()
		for _, item := range resp.Result.List {
			rec := buildOutcomeRecord(nil, item, nil)
			if recorded[rec.TradeID] {
				continue
			}
			AppendOutcomeRecord(rec)
			recorded[rec.TradeID] = true
		}
	}

	recs, _ := LoadOutcomeRecords()
	if len(recs) != 2 {
		t.Fatalf("after 3 passes: %d records, want 2 (dedup by trade_id failed)", len(recs))
	}
}

// A trade with no matching signal must be recorded as UNATTRIBUTED and
// flagged, not guessed at.
func TestUnattributedTradeIsFlagged(t *testing.T) {
	var resp struct {
		Result struct {
			List []closedPnLItem `json:"list"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(closedPnLFixture), &resp)

	rec := buildOutcomeRecord(nil, resp.Result.List[0], nil)
	if rec.StrategyPrimary != "UNATTRIBUTED" {
		t.Errorf("strategy_primary = %q, want UNATTRIBUTED", rec.StrategyPrimary)
	}
	if !contains(rec.Incomplete, "no_entry_context") {
		t.Errorf("incomplete = %v, want no_entry_context", rec.Incomplete)
	}
	if rec.ResultR != nil {
		t.Error("result_r must be null with no risk basis, not 0")
	}
}

func approx(a, b, tol float64) bool { d := a - b; return d < tol && d > -tol }
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
