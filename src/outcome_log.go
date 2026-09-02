package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"time"
)

// ─── Normalized completed-trade record ───────────────────────────────────
//
// outcome_log.jsonl is the ONLY source that statistics should be computed
// from. trade_history.json mixes real fills, pending orders, rejected orders
// and hand-written analysis notes in one array, so counting rows there
// produces a trade count that does not correspond to any set of trades that
// actually happened.
//
// One line here means: a position was opened and is now closed. Nothing else
// is written to this file.

const outcomeLogPath = "outcome_log.jsonl"

// OutcomeRecord is one COMPLETED trade.
//
// Fields carry an explicit provenance convention:
//   - a float that could not be determined is written as null (pointer), not 0,
//     so "no funding data" is never silently averaged in as "zero funding cost"
//   - Incomplete lists which enrichment steps failed, so a consumer can filter
//     on data quality instead of trusting every row equally
type OutcomeRecord struct {
	SchemaVersion int    `json:"schema_version"`
	TradeID       string `json:"trade_id"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"` // "BUY" | "SELL"

	// Strategy attribution, joined from trade_log.jsonl at entry time.
	StrategyPrimary      string   `json:"strategy_primary"`
	StrategiesConfirming []string `json:"strategies_confirming"`

	EntryTimestamp time.Time `json:"entry_timestamp"`
	EntryPrice     float64   `json:"entry_price"`
	ExitTimestamp  time.Time `json:"exit_timestamp"`
	ExitPrice      float64   `json:"exit_price"`
	Quantity       float64   `json:"quantity"`
	Notional       float64   `json:"notional"`

	// Cost decomposition. GrossPnL is before any cost; NetPnL is what the
	// account actually changed by.
	GrossPnL    float64  `json:"gross_pnl"`
	Fees        float64  `json:"fees"`         // always >= 0, a cost
	FundingCost *float64 `json:"funding_cost"` // null when the settlement log was unavailable
	Slippage    *float64 `json:"slippage"`     // null when no intended price was recorded
	NetPnL      float64  `json:"net_pnl"`

	// Risk normalisation. ResultR is net PnL expressed in units of the risk
	// taken at entry — the only PnL measure comparable across position sizes.
	InitialRiskUSD *float64 `json:"initial_risk_usd"`
	ResultR        *float64 `json:"result_r"`

	MaxAdverseExcursion   *float64 `json:"max_adverse_excursion"`   // worst unrealised drawdown, in R
	MaxFavorableExcursion *float64 `json:"max_favorable_excursion"` // best unrealised gain, in R

	Regime string `json:"regime"`

	// AI layers, logged whether or not they gated this trade. Counterfactual
	// analysis (Step 5) needs the call recorded even when it was ignored.
	KronosDirection  string   `json:"kronos_direction"`
	KronosConfidence *float64 `json:"kronos_confidence"`
	CouncilVerdict   string   `json:"council_verdict"`
	CouncilVotes     []string `json:"council_votes"`

	// WouldTradeWithoutAI is true when every non-AI gate passed, i.e. the
	// signal reached the AI layer on its own merits. It is the denominator
	// for "what does the AI layer actually change?"
	WouldTradeWithoutAI bool `json:"would_trade_without_ai"`

	ExitReason   string   `json:"exit_reason"`
	EquityBefore *float64 `json:"equity_before"`
	EquityAfter  *float64 `json:"equity_after"`

	// Provenance
	Source     string   `json:"source"`               // "bybit_closed_pnl" | "migration"
	Incomplete []string `json:"incomplete,omitempty"` // enrichment steps that failed
}

func f64p(v float64) *float64 { return &v }

// AppendOutcomeRecord writes one completed trade to outcome_log.jsonl.
//
// The path is relative to the working directory. It was previously
// "../outcome_log.jsonl" — the bot runs with cwd set to the repository root
// (see run-bot.sh), so every outcome record was being written one directory
// ABOVE the repo, outside version control. That is why outcome_log.jsonl
// appeared never to be produced.
func AppendOutcomeRecord(rec OutcomeRecord) error {
	rec.SchemaVersion = 2
	f, err := os.OpenFile(outcomeLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", outcomeLogPath, err)
	}
	defer f.Close()
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal outcome: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", outcomeLogPath, err)
	}
	return nil
}

// LoadOutcomeRecords reads every record in outcome_log.jsonl. Malformed lines
// are skipped and counted rather than aborting the load.
func LoadOutcomeRecords() ([]OutcomeRecord, int) {
	var recs []OutcomeRecord
	bad := 0
	data, err := os.ReadFile(outcomeLogPath)
	if err != nil {
		return recs, 0
	}
	for _, line := range splitLines(data) {
		var r OutcomeRecord
		if json.Unmarshal(line, &r) != nil {
			bad++
			continue
		}
		recs = append(recs, r)
	}
	return recs, bad
}

// LoadRecordedTradeIDs returns the set of trade_ids already written, so a
// re-run of the outcome tracker does not duplicate rows.
func LoadRecordedTradeIDs() map[string]bool {
	ids := make(map[string]bool)
	recs, _ := LoadOutcomeRecords()
	for _, r := range recs {
		ids[r.TradeID] = true
	}
	return ids
}

// ─── Entry-context join ──────────────────────────────────────────────────

// entryContext is the signal-time information for a trade, recovered from
// trade_log.jsonl so the outcome record can carry strategy and AI attribution
// that Bybit's closed-PnL endpoint does not know about.
type entryContext struct {
	found bool

	IntendedEntry        float64
	StrategyPrimary      string
	StrategiesConfirming []string
	Regime               string
	Conviction           int
	Confidence           float64
	RiskPct              float64
	WalletBal            float64
	KronosDirection      string
	KronosConfidence     float64
	HasKronos            bool
	CouncilVerdict       string
	CouncilVotes         []string
	WouldTradeWithoutAI  bool
}

// findEntryContext locates the executed trade_log.jsonl entry for a symbol
// whose timestamp is closest to (and not materially after) the fill time.
//
// The matching window is deliberately tight. A loose match would attribute a
// trade to the wrong signal, which is worse than no attribution at all: a
// mislabelled strategy silently corrupts per-strategy expectancy.
func findEntryContext(entries []TradeLogEntry, symbol string, entryTime time.Time) entryContext {
	const maxSkewBefore = 6 * time.Hour  // limit orders can rest a while before filling
	const maxSkewAfter = 5 * time.Minute // clock skew only

	var best *TradeLogEntry
	var bestGap time.Duration

	for i := range entries {
		e := entries[i]
		if e.Symbol != symbol || !e.Executed {
			continue
		}
		gap := entryTime.Sub(e.Timestamp)
		if gap < -maxSkewAfter || gap > maxSkewBefore {
			continue
		}
		if gap < 0 {
			gap = -gap
		}
		if best == nil || gap < bestGap {
			best = &entries[i]
			bestGap = gap
		}
	}

	if best == nil {
		return entryContext{}
	}

	ctx := entryContext{
		found:           true,
		IntendedEntry:   best.EntryPrice,
		StrategyPrimary: best.Strategy,
		Regime:          best.Regime,
		Conviction:      best.Conviction,
		Confidence:      best.Confidence,
		RiskPct:         best.RiskPct,
		WalletBal:       best.WalletBal,
		CouncilVerdict:  best.AIVerdict,
		CouncilVotes:    best.CouncilVotes,
		KronosDirection: best.KronosDirection,
		// The trade reached the AI gate, so every indicator/risk gate ahead of
		// it passed. That is exactly the "would have traded without AI" set.
		WouldTradeWithoutAI: true,
	}
	if best.KronosConfidence > 0 {
		ctx.KronosConfidence = best.KronosConfidence
		ctx.HasKronos = true
	}
	if best.S4Active {
		ctx.StrategiesConfirming = append(ctx.StrategiesConfirming, "S4")
	}
	if best.S5Active {
		ctx.StrategiesConfirming = append(ctx.StrategiesConfirming, "S5")
	}
	ctx.StrategiesConfirming = append(ctx.StrategiesConfirming, best.ConfirmingStrategies...)
	sort.Strings(ctx.StrategiesConfirming)
	return ctx
}

// LoadTradeLogEntries reads trade_log.jsonl into memory.
func LoadTradeLogEntries() []TradeLogEntry {
	var out []TradeLogEntry
	data, err := os.ReadFile(tradeLogPath)
	if err != nil {
		return out
	}
	for _, line := range splitLines(data) {
		var e TradeLogEntry
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// ─── Excursion measurement ───────────────────────────────────────────────

// computeExcursions replays 15-minute candles across the hold window and
// returns MAE and MFE in units of R (initial risk). Returns nils when the
// candle data or the risk basis is unavailable — an unmeasured excursion is
// recorded as unknown, never as zero.
func computeExcursions(client *BybitClient, symbol, side string, entryPrice, riskPerUnit float64,
	entryTime, exitTime time.Time) (mae *float64, mfe *float64, err error) {

	if riskPerUnit <= 0 || entryPrice <= 0 {
		return nil, nil, fmt.Errorf("no risk basis for excursion measurement")
	}

	candles, err := client.FetchKlines(symbol, "15", 1000)
	if err != nil {
		return nil, nil, fmt.Errorf("kline fetch: %w", err)
	}

	worst, best := 0.0, 0.0
	seen := 0
	for _, c := range candles {
		if c.Timestamp.Before(entryTime) || c.Timestamp.After(exitTime) {
			continue
		}
		seen++
		var adverse, favorable float64
		if side == "BUY" {
			adverse = entryPrice - c.Low
			favorable = c.High - entryPrice
		} else {
			adverse = c.High - entryPrice
			favorable = entryPrice - c.Low
		}
		if adverse > worst {
			worst = adverse
		}
		if favorable > best {
			best = favorable
		}
	}
	if seen == 0 {
		return nil, nil, fmt.Errorf("no candles inside hold window %s..%s",
			entryTime.Format(time.RFC3339), exitTime.Format(time.RFC3339))
	}

	return f64p(worst / riskPerUnit), f64p(best / riskPerUnit), nil
}

// ─── Funding cost ────────────────────────────────────────────────────────

// fetchFundingCost sums SETTLEMENT entries from Bybit's transaction log for
// one symbol over the hold window. A positive return means funding COST the
// account; negative means funding was received.
func fetchFundingCost(client *BybitClient, symbol string, entryTime, exitTime time.Time) (float64, error) {
	endpoint := fmt.Sprintf(
		"/v5/account/transaction-log?accountType=UNIFIED&category=linear&currency=USDT&type=SETTLEMENT&symbol=%s&startTime=%d&endTime=%d&limit=50",
		symbol, entryTime.UnixMilli(), exitTime.UnixMilli())

	respBytes, err := client.GetPrivateRequest(endpoint)
	if err != nil {
		return 0, fmt.Errorf("transaction log: %w", err)
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol   string `json:"symbol"`
				Type     string `json:"type"`
				Funding  string `json:"funding"`
				CashFlow string `json:"cashFlow"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return 0, fmt.Errorf("parse transaction log: %w", err)
	}
	if resp.RetCode != 0 {
		return 0, fmt.Errorf("bybit error [%d]: %s", resp.RetCode, resp.RetMsg)
	}

	total := 0.0
	for _, item := range resp.Result.List {
		if item.Symbol != symbol {
			continue
		}
		// Bybit reports funding as a signed cash flow: negative = paid out.
		// Flip the sign so the field reads as a COST.
		v, perr := strconv.ParseFloat(item.Funding, 64)
		if perr != nil || item.Funding == "" {
			v, perr = strconv.ParseFloat(item.CashFlow, 64)
			if perr != nil {
				continue
			}
		}
		total -= v
	}
	return total, nil
}

// ─── Assembly ────────────────────────────────────────────────────────────

// closedPnLItem is the subset of Bybit's /v5/position/closed-pnl response we use.
type closedPnLItem struct {
	Symbol        string `json:"symbol"`
	OrderID       string `json:"orderId"`
	Side          string `json:"side"` // side that CLOSED the position
	OrderType     string `json:"orderType"`
	ExecType      string `json:"execType"`
	ClosedSize    string `json:"closedSize"`
	CumEntryValue string `json:"cumEntryValue"`
	AvgEntryPrice string `json:"avgEntryPrice"`
	CumExitValue  string `json:"cumExitValue"`
	AvgExitPrice  string `json:"avgExitPrice"`
	ClosedPnl     string `json:"closedPnl"`
	OpenFee       string `json:"openFee"`
	CloseFee      string `json:"closeFee"`
	Leverage      string `json:"leverage"`
	CreatedTime   string `json:"createdTime"`
	UpdatedTime   string `json:"updatedTime"`
}

// buildOutcomeRecord turns one Bybit closed-PnL row plus its entry context
// into a normalized record. Enrichment failures degrade individual fields to
// null and are named in Incomplete; they never drop the trade.
func buildOutcomeRecord(client *BybitClient, item closedPnLItem, entries []TradeLogEntry) OutcomeRecord {
	pf := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

	entryPrice := pf(item.AvgEntryPrice)
	exitPrice := pf(item.AvgExitPrice)
	qty := pf(item.ClosedSize)
	closedPnL := pf(item.ClosedPnl)
	openFee := math.Abs(pf(item.OpenFee))
	closeFee := math.Abs(pf(item.CloseFee))

	entryTime := time.UnixMilli(parseInt64(item.CreatedTime)).UTC()
	exitTime := time.UnixMilli(parseInt64(item.UpdatedTime)).UTC()

	// Bybit's "side" on a closed-PnL row is the side that CLOSED the position,
	// so the position's own direction is the opposite.
	side := "BUY"
	if item.Side == "Buy" {
		side = "SELL"
	}

	notional := pf(item.CumEntryValue)
	if notional == 0 {
		notional = entryPrice * qty
	}

	fees := openFee + closeFee
	// Bybit's closedPnl is already net of trading fees.
	netPnL := closedPnL
	grossPnL := closedPnL + fees

	rec := OutcomeRecord{
		TradeID:        item.Symbol + "_" + item.UpdatedTime,
		Symbol:         item.Symbol,
		Side:           side,
		EntryTimestamp: entryTime,
		EntryPrice:     entryPrice,
		ExitTimestamp:  exitTime,
		ExitPrice:      exitPrice,
		Quantity:       qty,
		Notional:       notional,
		GrossPnL:       grossPnL,
		Fees:           fees,
		NetPnL:         netPnL,
		ExitReason:     classifyOutcome(item.OrderType, item.ExecType, closedPnL),
		Source:         "bybit_closed_pnl",
	}
	if item.OrderID != "" {
		rec.TradeID = item.Symbol + "_" + item.OrderID
	}

	ctx := findEntryContext(entries, item.Symbol, entryTime)
	if !ctx.found {
		rec.Incomplete = append(rec.Incomplete, "no_entry_context")
		rec.StrategyPrimary = "UNATTRIBUTED"
	} else {
		rec.StrategyPrimary = ctx.StrategyPrimary
		rec.StrategiesConfirming = ctx.StrategiesConfirming
		rec.Regime = ctx.Regime
		rec.CouncilVerdict = ctx.CouncilVerdict
		rec.CouncilVotes = ctx.CouncilVotes
		rec.KronosDirection = ctx.KronosDirection
		rec.WouldTradeWithoutAI = ctx.WouldTradeWithoutAI
		if ctx.HasKronos {
			rec.KronosConfidence = f64p(ctx.KronosConfidence)
		}
		if ctx.IntendedEntry > 0 && entryPrice > 0 {
			// Slippage as a signed cost: positive means the fill was worse
			// than intended for the direction actually taken.
			slip := entryPrice - ctx.IntendedEntry
			if side == "SELL" {
				slip = ctx.IntendedEntry - entryPrice
			}
			rec.Slippage = f64p(slip * qty)
		} else {
			rec.Incomplete = append(rec.Incomplete, "no_intended_entry_price")
		}
		if ctx.RiskPct > 0 && ctx.WalletBal > 0 {
			risk := ctx.WalletBal * ctx.RiskPct
			rec.InitialRiskUSD = f64p(risk)
			if risk > 0 {
				rec.ResultR = f64p(netPnL / risk)
			}
			rec.EquityBefore = f64p(ctx.WalletBal)
			rec.EquityAfter = f64p(ctx.WalletBal + netPnL)
		} else {
			rec.Incomplete = append(rec.Incomplete, "no_risk_basis")
		}
	}

	if client == nil {
		rec.Incomplete = append(rec.Incomplete, "no_client_no_funding", "no_client_no_excursions")
		return rec
	}

	if fc, err := fetchFundingCost(client, item.Symbol, entryTime, exitTime); err != nil {
		rec.Incomplete = append(rec.Incomplete, "funding_unavailable")
	} else {
		rec.FundingCost = f64p(fc)
		// Bybit's closedPnl excludes funding settlements, so net PnL must
		// absorb them separately.
		rec.NetPnL = netPnL - fc
		if rec.InitialRiskUSD != nil && *rec.InitialRiskUSD > 0 {
			rec.ResultR = f64p(rec.NetPnL / *rec.InitialRiskUSD)
		}
		if rec.EquityBefore != nil {
			rec.EquityAfter = f64p(*rec.EquityBefore + rec.NetPnL)
		}
	}

	riskPerUnit := 0.0
	if rec.InitialRiskUSD != nil && qty > 0 {
		riskPerUnit = *rec.InitialRiskUSD / qty
	}
	mae, mfe, err := computeExcursions(client, item.Symbol, side, entryPrice, riskPerUnit, entryTime, exitTime)
	if err != nil {
		rec.Incomplete = append(rec.Incomplete, "excursions_unavailable")
	} else {
		rec.MaxAdverseExcursion = mae
		rec.MaxFavorableExcursion = mfe
	}

	return rec
}

func parseInt64(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
