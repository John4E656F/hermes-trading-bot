package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

func ParseAndComputeOI(raw []byte) OISnapshot {
	var res struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				OpenInterest string `json:"openInterest"`
				Timestamp    string `json:"timestamp"`
			} `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(raw, &res); err != nil || res.RetCode != 0 || len(res.Result.List) == 0 {
		return OISnapshot{}
	}

	var data []OIDataPoint
	for _, item := range res.Result.List {
		oi, _ := strconv.ParseFloat(item.OpenInterest, 64)
		ts, _ := strconv.ParseInt(item.Timestamp, 10, 64)
		data = append(data, OIDataPoint{OpenInterest: oi, Timestamp: ts})
	}

	if len(data) == 0 {
		return OISnapshot{}
	}

	currentOI := data[0].OpenInterest
	pastIdx := 6
	if pastIdx >= len(data) {
		pastIdx = len(data) - 1
	}
	pastOI := data[pastIdx].OpenInterest
	change24h := 0.0
	if pastOI > 0 {
		change24h = ((currentOI - pastOI) / pastOI) * 100.0
	}

	return OISnapshot{
		Current:   currentOI,
		Change24h: change24h,
		IsSpike:   math.Abs(change24h) > 8.0,
	}
}

func ParseAndComputeFunding(raw []byte) FundingSnapshot {
	var res struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				FundingRate          string `json:"fundingRate"`
				FundingRateTimestamp string `json:"fundingRateTimestamp"`
			} `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(raw, &res); err != nil || res.RetCode != 0 || len(res.Result.List) == 0 {
		return FundingSnapshot{}
	}

	currentRate, _ := strconv.ParseFloat(res.Result.List[0].FundingRate, 64)

	// Raised thresholds for genuine extremes:
	// Negative extreme: shorts paying > 0.02%/8h = 0.06%/day = ~22%/year → squeeze risk
	// Positive extreme: longs paying > 0.05%/8h = 0.15%/day = ~55%/year → dump risk
	isNeg := currentRate < -0.0002
	isPos := currentRate > 0.0005

	return FundingSnapshot{
		CurrentRate: currentRate,
		IsNegative:  isNeg,
		IsPositive:  isPos,
		IsExtreme:   isNeg || isPos,
	}
}

// EvaluateS2Squeeze uses OI spikes combined with funding extremes to detect
// short/long squeeze setups (unchanged from original logic).
func EvaluateS2Squeeze(oi OISnapshot, funding FundingSnapshot, price float64, ema20 float64) S2Signal {
	sig := S2Signal{Active: false, Action: ACTION_HOLD, Reason: "No squeeze setup detected"}

	if oi.Current == 0 || math.Abs(funding.CurrentRate) == 0 {
		sig.Reason = "Missing OI/Funding data for S2"
		return sig
	}

	if oi.IsSpike {
		if funding.IsNegative && price < ema20 {
			sig.Active = true
			sig.Action = ACTION_BUY
			sig.SqueezeType = "Short Squeeze"
			sig.Reason = "OI Spike + Extreme Negative Funding + Below EMA20. High probability short squeeze."
		} else if funding.IsPositive && price > ema20 {
			sig.Active = true
			sig.Action = ACTION_SELL
			sig.SqueezeType = "Long Squeeze"
			sig.Reason = "OI Spike + Extreme Positive Funding + Above EMA20. High probability long squeeze."
		}
	}

	return sig
}

// EvaluateS4FundingContrarian is a LEADING signal based on funding rate extremes.
//
// The edge: When perpetual funding is extremely positive, longs are paying shorts
// ~0.15%/day (55%/year). This is economically unsustainable — overleveraged longs
// WILL de-risk or get liquidated, pushing price down. The reverse holds for shorts.
//
// This signal fires independently of price action (it's predictive, not lagging).
// Source of edge: market microstructure inefficiency, not technical analysis.
func EvaluateS4FundingContrarian(funding FundingSnapshot, oi OISnapshot) S4Signal {
	sig := S4Signal{FundingRate: funding.CurrentRate}

	if funding.CurrentRate == 0 {
		sig.Reason = "No funding data available"
		return sig
	}

	dailyCost := math.Abs(funding.CurrentRate) * 3 * 100 // 3 periods/day → daily %

	// EXTREME POSITIVE: longs paying > 0.05%/8h. Market overleveraged LONG.
	// Longs must de-risk or face liquidation → contrarian SELL.
	if funding.CurrentRate > 0.0005 {
		sig.Active = true
		sig.Action = ACTION_SELL
		sig.Reason = fmt.Sprintf(
			"FUNDING CONTRARIAN SELL: rate +%.4f%% (longs paying %.3f%%/day). Market overleveraged LONG — squeeze imminent.",
			funding.CurrentRate*100, dailyCost)
		return sig
	}

	// EXTREME NEGATIVE: shorts paying > 0.02%/8h. Market overleveraged SHORT.
	// Shorts must de-risk or face liquidation → contrarian BUY (short squeeze).
	if funding.CurrentRate < -0.0002 {
		sig.Active = true
		sig.Action = ACTION_BUY
		sig.Reason = fmt.Sprintf(
			"FUNDING CONTRARIAN BUY: rate %.4f%% (shorts paying %.3f%%/day). Market overleveraged SHORT — squeeze imminent.",
			funding.CurrentRate*100, dailyCost)
		return sig
	}

	sig.Reason = fmt.Sprintf("Funding rate %.5f%% — no extreme detected", funding.CurrentRate*100)
	return sig
}
