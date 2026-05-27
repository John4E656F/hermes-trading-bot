package main

import (
	"encoding/json"
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
	
	// Bybit returns list in descending order of time.
	// Find OI 24h ago. Data is 4H interval, so index 6 is ~24h ago.
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

	currentRateStr := res.Result.List[0].FundingRate
	currentRate, _ := strconv.ParseFloat(currentRateStr, 64)

	// Thresholds: Negative < -0.01% (-0.0001), Positive > 0.03% (0.0003)
	isNeg := currentRate < -0.0001
	isPos := currentRate > 0.0003

	return FundingSnapshot{
		CurrentRate: currentRate,
		IsNegative:  isNeg,
		IsPositive:  isPos,
		IsExtreme:   isNeg || isPos,
	}
}

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
			sig.Reason = "OI Spike + Deep Negative Funding + Below EMA20. High probability of short squeeze."
		} else if funding.IsPositive && price > ema20 {
			sig.Active = true
			sig.Action = ACTION_SELL
			sig.SqueezeType = "Long Squeeze"
			sig.Reason = "OI Spike + High Positive Funding + Above EMA20. High probability of long squeeze."
		}
	}

	return sig
}
