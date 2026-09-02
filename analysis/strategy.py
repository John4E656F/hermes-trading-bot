"""
Port of src/allocator.go's signal engine, plus the individual S1/S2/S3 lenses.

Scope note: the AI layers (Kronos prime/overlay, AI Council, reflection memory,
sentiment) are NOT part of the blended baseline here. They are non-deterministic
external calls that cannot be replayed from historical OHLCV. Step 5 measures
them separately as counterfactuals against recorded verdicts. The "blended"
configuration below is therefore the deterministic indicator stack exactly as
allocator.go computes it when Kronos returns hold/unavailable.
"""

import indicators as ind

HOLD, BUY, SELL = "HOLD", "BUY", "SELL"


# ─── Sub-strategies (ports of the Evaluate* functions) ───────────────────

def eval_s1(price, vp):
    """S1 Mean Reversion — volume-profile value-area edges."""
    if not vp or vp["total"] == 0:
        return {"active": False, "action": HOLD}
    if price <= vp["val"]:
        return {"active": True, "action": BUY}
    if price >= vp["vah"]:
        return {"active": True, "action": SELL}
    return {"active": False, "action": HOLD}


def eval_s2(oi_change_24h, oi_current, funding_rate, price, ema20):
    """S2 OI/Funding Squeeze."""
    if not oi_current or funding_rate is None or funding_rate == 0:
        return {"active": False, "action": HOLD}
    is_spike = abs(oi_change_24h) > 8.0
    if not is_spike:
        return {"active": False, "action": HOLD}
    if funding_rate < -0.0005 and price < ema20:
        return {"active": True, "action": BUY}
    if funding_rate > 0.0005 and price > ema20:
        return {"active": True, "action": SELL}
    return {"active": False, "action": HOLD}


def eval_s3(price, consol, latest_vol, avg_vol):
    """S3 Consolidation Breakout."""
    if not consol or not consol["is_consolidating"]:
        return {"active": False, "action": HOLD}
    vol_ok = avg_vol > 0 and latest_vol >= avg_vol * 1.5
    if price > consol["high"] and vol_ok:
        return {"active": True, "action": BUY}
    if price < consol["low"] and vol_ok:
        return {"active": True, "action": SELL}
    return {"active": False, "action": HOLD}


def eval_s4(funding_rate):
    """S4 Funding Contrarian."""
    if funding_rate is None or funding_rate == 0:
        return {"active": False, "action": HOLD}
    if funding_rate > 0.0005:
        return {"active": True, "action": SELL}
    if funding_rate < -0.0005:
        return {"active": True, "action": BUY}
    return {"active": False, "action": HOLD}


def eval_s5(bb_upper, bb_basis, bb_lower, price, latest_vol, avg_vol):
    """S5 Bollinger Squeeze Breakout."""
    if bb_basis == 0 or avg_vol == 0:
        return {"active": False, "action": HOLD}
    width = (bb_upper - bb_lower) / bb_basis * 100.0
    if width >= 4.0:
        return {"active": False, "action": HOLD}
    vol_ok = latest_vol >= avg_vol * 1.5
    if price > bb_upper and vol_ok:
        return {"active": True, "action": BUY}
    if price < bb_lower and vol_ok:
        return {"active": True, "action": SELL}
    return {"active": False, "action": HOLD}


# ─── Bar state ───────────────────────────────────────────────────────────

def compute_bar_state(c4h, c1d, funding_rate, oi_change_24h, oi_current):
    """Everything allocator.go reads for one asset at one bar close."""
    price = c4h[-1]["close"]
    ema20 = ind.ema(c4h, 20)
    sma50 = ind.sma(c4h, 50)
    rsi14 = ind.rsi(c4h, 14)
    wr = ind.williams_r(c4h, 14)
    vwap20 = ind.vwap(c4h, 20)
    atr14 = ind.atr(c4h, 14)
    bb_u, bb_b, bb_l, bb_w = ind.bollinger(c4h, 20, 2.0)
    avg_vol = ind.volume_ma(c4h, 20)
    latest_vol = c4h[-1]["volume"]
    vol_ratio = latest_vol / avg_vol if avg_vol > 0 else 0.0

    daily_adx = ind.adx(c1d, 14) if len(c1d) >= 28 else 0.0
    vp = ind.volume_profile(c1d, 30)
    consol = ind.consolidation(c1d, 21, 5.0)
    g7 = ind.gain_7d(c1d)

    return {
        "price": price, "ema20": ema20, "sma50": sma50, "rsi14": rsi14,
        "wr": wr, "vwap20": vwap20, "atr14": atr14,
        "bb_upper": bb_u, "bb_basis": bb_b, "bb_lower": bb_l, "bb_width": bb_w,
        "avg_vol": avg_vol, "latest_vol": latest_vol, "vol_ratio": vol_ratio,
        "daily_adx": daily_adx, "regime": ind.classify_regime(daily_adx),
        "vp": vp, "consol": consol, "gain7d": g7,
        "funding": funding_rate, "oi_change_24h": oi_change_24h,
        "s1": eval_s1(price, vp),
        "s2": eval_s2(oi_change_24h, oi_current, funding_rate, price, ema20),
        "s3": eval_s3(price, consol, latest_vol, avg_vol),
        "s4": eval_s4(funding_rate),
        "s5": eval_s5(bb_u, bb_b, bb_l, price, latest_vol, avg_vol),
    }


# ─── Master chain (port of EvaluateMarketSnapshot, AI layers excluded) ───

def master_signal(st):
    """Returns {action, strategy, conviction, confidence} or a HOLD."""
    hold = {"action": HOLD, "strategy": "HOLD", "conviction": 0, "confidence": 0.0}

    price, ema20, sma50 = st["price"], st["ema20"], st["sma50"]
    rsi14, wr, vwap20 = st["rsi14"], st["wr"], st["vwap20"]
    adx_d, g7, bbw = st["daily_adx"], st["gain7d"], st["bb_width"]
    vol_ratio = st["vol_ratio"]
    vol_surge = vol_ratio >= 1.5
    s1, s2, s3, s4, s5 = st["s1"], st["s2"], st["s3"], st["s4"], st["s5"]

    action, strategy = HOLD, "HOLD"

    # TIER 1: S4 funding contrarian, with the SMA50 price-confirmation guard.
    if s4["active"]:
        blocked = False
        if s4["action"] == BUY and sma50 > 0 and price < sma50 * 0.98:
            blocked = True
        elif s4["action"] == SELL and sma50 > 0 and price > sma50 * 1.02:
            blocked = True
        if not blocked:
            action, strategy = s4["action"], "S4 Funding Contrarian"

    # TIER 2: S5 squeeze breakout (only over HOLD).
    if action == HOLD and s5["active"]:
        action, strategy = s5["action"], "S5 BB Squeeze"

    # TIER 3: strong trend.
    if action == HOLD:
        if adx_d > 40 and price < ema20 and wr > -70:
            action, strategy = SELL, "Trend Sell"
        elif adx_d > 40 and price > ema20 and wr < -70 and g7 > -10.0:
            action, strategy = BUY, "Trend Buy"

    # TIER 4: volume-confirmed strict signals.
    if action == HOLD:
        vwap_bear = vwap20 == 0 or price < vwap20
        if price < ema20 and price < sma50 and vwap_bear and rsi14 < 50 \
                and adx_d > 25 and vol_surge:
            action, strategy = SELL, "Strict Sell"
        vwap_bull = vwap20 == 0 or price > vwap20
        if price > ema20 and price > sma50 and vwap_bull and rsi14 > 50 \
                and adx_d > 25 and vol_surge and g7 > -10.0:
            action, strategy = BUY, "Strict Buy"

    # TIER 5: mean-reversion extremes.
    if action == HOLD and ema20 > 0:
        dist = abs(price - ema20) / ema20
        if wr <= -85 and rsi14 < 30 and vol_ratio >= 2.0 \
                and st["regime"] == "RANGING" and dist <= 0.05 and g7 > -10.0:
            action, strategy = BUY, "Oversold Buy"
        if wr >= -15 and rsi14 > 70 and vol_ratio >= 2.0 \
                and st["regime"] == "RANGING" and dist <= 0.05:
            action, strategy = SELL, "Overbought Sell"

    # Exhaustion filter.
    if action == BUY and g7 > 40.0 and adx_d < 50:
        return hold
    if action == SELL and g7 < -15.0 and adx_d < 50:
        return hold

    # BB squeeze lock.
    if action != HOLD and strategy != "S5 BB Squeeze" and 0 < bbw < 2.0:
        return hold

    # Funding block.
    if s4["active"] and s4["action"] != action and action != HOLD:
        return hold

    if action == HOLD:
        return hold

    # S0 independent verification.
    s0 = False
    if action == BUY:
        above_vwap = vwap20 == 0 or price > vwap20
        s0 = (rsi14 > 50 and price > ema20 and above_vwap and wr < -40) or adx_d > 40
    else:
        below_vwap = vwap20 == 0 or price < vwap20
        s0 = (rsi14 < 50 and price < ema20 and below_vwap and wr > -80) or adx_d > 40

    agree = 1 if s0 else 0
    for sub in (s1, s2, s3, s4, s5):
        if sub["active"] and sub["action"] == action:
            agree += 1

    if agree <= 0:
        return hold

    if agree == 1:
        boost = False
        if adx_d > 40:
            boost = (action == BUY and wr < -60) or (action == SELL and wr > -40)
        if not boost and vol_ratio >= 2.0:
            boost = (action == BUY and wr < -65) or (action == SELL and wr > -35)
        if not boost and vol_ratio >= 1.5:
            boost = (action == BUY and wr <= -90) or (action == SELL and wr >= -10)
        if boost:
            conviction, confidence, strategy = 2, 0.70, "QUALITY: " + strategy
        else:
            conviction, confidence = 1, 0.60
    elif agree == 2:
        conviction, confidence, strategy = 2, 0.75, "CONFIRMED: " + strategy
    else:
        conviction, confidence, strategy = 3, 0.85, "META: " + strategy

    # Funding headwind penalty.
    fr = st["funding"] or 0.0
    if action == BUY and fr > 0.0003:
        confidence *= 0.80
    if action == SELL and fr < -0.0001:
        confidence *= 0.80

    # S4 tailwind bonus.
    if s4["active"] and s4["action"] == action and conviction == 2:
        conviction = 3
        confidence = min(confidence + 0.05, 0.90)
        strategy = "META: " + strategy.split(": ")[-1] + " + S4"

    # Risk cap on extended moves.
    if 15.0 < g7 <= 40.0 and confidence > 0.60:
        confidence = min(confidence, 0.65)
    elif -40.0 <= g7 < -10.0 and confidence > 0.60:
        confidence = min(confidence, 0.65)

    return {"action": action, "strategy": strategy,
            "conviction": conviction, "confidence": confidence}
