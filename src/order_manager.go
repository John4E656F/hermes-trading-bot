package main

import (
	"encoding/json"
	"fmt"
	"time"
)

type openOrder struct {
	OrderID string
	Symbol  string
	Side    string // "Buy" or "Sell"
	Price   string
	Qty     string
}

// ManageStaleLimitOrders fetches all open GTC limit orders, re-evaluates the current
// signal for each, and cancels any where direction has reversed or gone HOLD.
//
// Returns a map of symbol → side for orders that are still valid and open — the
// execution loop uses this to prevent double-entry on an already-queued symbol.
func ManageStaleLimitOrders(client *BybitClient, signals map[string]StrategySignal) map[string]string {
	activeOrders := make(map[string]string) // symbol → "Buy"/"Sell"

	respBytes, err := client.GetPrivateRequest("/v5/order/realtime?category=linear&settleCoin=USDT&limit=50")
	if err != nil {
		fmt.Printf("⚠️ Open order fetch failed: %v\n", err)
		return activeOrders
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				OrderID   string `json:"orderId"`
				Symbol    string `json:"symbol"`
				Side      string `json:"side"`
				OrderType string `json:"orderType"`
				Price     string `json:"price"`
				Qty       string `json:"qty"`
			} `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(respBytes, &resp); err != nil || resp.RetCode != 0 {
		return activeOrders
	}

	if len(resp.Result.List) == 0 {
		return activeOrders
	}

	fmt.Printf("🔍 Stale order audit: %d open limit order(s) found.\n", len(resp.Result.List))

	for _, o := range resp.Result.List {
		if o.OrderType != "Limit" {
			continue
		}

		sig, known := signals[o.Symbol]

		// If we have no signal for this symbol (not in watchlist), cancel defensively.
		stale := false
		reason := ""
		if !known {
			stale = true
			reason = "symbol not in current watchlist"
		} else if o.Side == "Buy" && sig.Action != ACTION_BUY {
			stale = true
			reason = fmt.Sprintf("signal flipped to %s — %s", sig.Action, sig.Reason)
		} else if o.Side == "Sell" && sig.Action != ACTION_SELL {
			stale = true
			reason = fmt.Sprintf("signal flipped to %s — %s", sig.Action, sig.Reason)
		}

		if !stale {
			fmt.Printf("   ✅ KEEPING  %s %s limit @ %s — signal still %s\n",
				o.Symbol, o.Side, o.Price, sig.Action)
			activeOrders[o.Symbol] = o.Side // block double-entry
			continue
		}

		// Cancel the stale order.
		cancelBytes, cancelErr := client.PostPrivateRequest("/v5/order/cancel", map[string]interface{}{
			"category": "linear",
			"symbol":   o.Symbol,
			"orderId":  o.OrderID,
		})
		if cancelErr != nil {
			fmt.Printf("   ⚠️ Cancel API call failed for %s %s: %v\n", o.Symbol, o.OrderID, cancelErr)
			activeOrders[o.Symbol] = o.Side // treat as still open to avoid double-entry
			continue
		}

		var cancelRes struct {
			RetCode int    `json:"retCode"`
			RetMsg  string `json:"retMsg"`
		}
		json.Unmarshal(cancelBytes, &cancelRes)

		if cancelRes.RetCode == 0 {
			fmt.Printf("   🚫 CANCELLED %s %s limit @ %s — %s\n",
				o.Symbol, o.Side, o.Price, reason)

			// Log the cancellation so we can track how often signals reverse.
			AppendTradeLog(TradeLogEntry{
				Timestamp:  time.Now().UTC(),
				Symbol:     o.Symbol,
				Side:       o.Side,
				OrderType:  "Limit",
				EntryPrice: 0, // was never filled
				Qty:        o.Qty,
				Executed:   false,
				SkipReason: "stale order cancelled: " + reason,
				Strategy:   func() string {
					if known {
						return sig.Strategy
					}
					return "unknown"
				}(),
			})
		} else {
			fmt.Printf("   ⚠️ Cancel rejected for %s: %s (code %d)\n",
				o.Symbol, cancelRes.RetMsg, cancelRes.RetCode)
			activeOrders[o.Symbol] = o.Side // order still alive — don't double-enter
		}
	}

	return activeOrders
}
