package main

type StrategySignal struct {
	Symbol     string
	Regime     MarketRegime // retained for dashboard display
	Strategy   string       // "S1: Mean Reversion" / "S2: OI Squeeze" / "S3: Breakout" / "META: Liquidation Breakout"
	Action     SignalAction
	Reason     string
	Conviction int     // 1, 2, or 3
	Confidence float64 // mapped from conviction
	S1         S1Signal
	S2         S2Signal
	S3         S3Signal
}

func EvaluateMarketSnapshot(asset *AssetSnapshot) StrategySignal {
	dailyADX := asset.Snap1d.Indicators.ADX14
	currentRegime := ClassifyRegime(dailyADX)

	snap4h := asset.Snap4h
	latestPrice := snap4h.Candles[len(snap4h.Candles)-1].Close
	ema20 := snap4h.Indicators.EMA20

	avgVol := CalculateVolumeMA(snap4h.Candles, 20)
	latestVol := snap4h.Candles[len(snap4h.Candles)-1].Volume

	s1 := EvaluateS1MeanReversion(latestPrice, asset.VP)
	s2 := EvaluateS2Squeeze(asset.OI, asset.Funding, latestPrice, ema20)
	s3 := EvaluateS3Breakout(latestPrice, asset.Consolidation, latestVol, avgVol)

	signal := StrategySignal{
		Symbol: asset.Symbol,
		Regime: currentRegime,
		Action: ACTION_HOLD,
		S1:     s1,
		S2:     s2,
		S3:     s3,
	}

	buyCount := 0
	sellCount := 0

	if s1.Active {
		if s1.Action == ACTION_BUY {
			buyCount++
		} else if s1.Action == ACTION_SELL {
			sellCount++
		}
	}
	if s2.Active {
		if s2.Action == ACTION_BUY {
			buyCount++
		} else if s2.Action == ACTION_SELL {
			sellCount++
		}
	}
	if s3.Active {
		if s3.Action == ACTION_BUY {
			buyCount++
		} else if s3.Action == ACTION_SELL {
			sellCount++
		}
	}

	if buyCount > sellCount && buyCount > 0 {
		signal.Action = ACTION_BUY
		signal.Conviction = buyCount
	} else if sellCount > buyCount && sellCount > 0 {
		signal.Action = ACTION_SELL
		signal.Conviction = sellCount
	} else {
		signal.Action = ACTION_HOLD
		signal.Conviction = 0
		signal.Confidence = 0.0
		signal.Reason = "No clear alignment across strategies."
		return signal
	}

	switch signal.Conviction {
	case 1:
		signal.Confidence = 0.60
		if s1.Active && s1.Action == signal.Action {
			signal.Strategy = "S1: Mean Reversion"
			signal.Reason = s1.Reason
		} else if s2.Active && s2.Action == signal.Action {
			signal.Strategy = "S2: OI Squeeze"
			signal.Reason = s2.Reason
		} else if s3.Active && s3.Action == signal.Action {
			signal.Strategy = "S3: Breakout"
			signal.Reason = s3.Reason
		}
	case 2:
		signal.Confidence = 0.75
		signal.Strategy = "Double Alignment"
		signal.Reason = "Two independent strategies aligned, providing elevated probability."
	case 3:
		signal.Confidence = 0.90
		signal.Strategy = "META: Liquidation Breakout"
		signal.Reason = "Perfect meta-alignment: Consolidation breakout + OI squeeze + Value Area deviation."
	}

	return signal
}
