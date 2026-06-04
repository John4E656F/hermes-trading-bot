package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// TradeLogEntry captures everything needed to replay, audit, and improve a signal.
type TradeLogEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Symbol       string    `json:"symbol"`
	Side         string    `json:"side"`
	OrderType    string    `json:"order_type"`
	EntryPrice   float64   `json:"entry_price"`
	Qty          string    `json:"qty"`
	StopLoss     string    `json:"stop_loss"`
	TakeProfit   string    `json:"take_profit"`
	ATR          float64   `json:"atr"`
	ADX          float64   `json:"adx"`
	RSI          float64   `json:"rsi"`
	WilliamsR    float64   `json:"williams_r"`
	BBWidth      float64   `json:"bb_width"`
	FundingRate  float64   `json:"funding_rate"`
	S4Active     bool      `json:"s4_active"`
	S5Active     bool      `json:"s5_active"`
	Conviction   int       `json:"conviction"`
	Confidence   float64   `json:"confidence"`
	RiskPct      float64   `json:"risk_pct"`
	Strategy     string    `json:"strategy"`
	Reason       string    `json:"reason"`
	WalletBal    float64   `json:"wallet_balance"`
	AIVerdict    string    `json:"ai_verdict"`
	AIConfidence float64   `json:"ai_confidence"`
	AIReason     string    `json:"ai_reason"`
	Executed     bool      `json:"executed"`
	SkipReason   string    `json:"skip_reason,omitempty"`
}

const tradeLogPath = "../trade_log.jsonl"

// AppendTradeLog writes one JSON line per trade to trade_log.jsonl.
// Each line is a self-contained record — easy to grep, tail, or import to pandas.
func AppendTradeLog(entry TradeLogEntry) {
	f, err := os.OpenFile(tradeLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("   ⚠️ Trade log write failed: %v\n", err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Printf("   ⚠️ Trade log marshal failed: %v\n", err)
		return
	}
	f.Write(append(data, '\n'))
	fmt.Printf("   📝 Trade logged → %s\n", tradeLogPath)
}
