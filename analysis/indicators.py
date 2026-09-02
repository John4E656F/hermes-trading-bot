"""
Ports of src/indicators.go, src/volume_profile.go, src/consolidation.go and
src/oi_funding.go.

These are deliberate line-by-line ports rather than idiomatic reimplementations.
A backtest that uses textbook indicators while the bot uses its own variants
measures a different system than the one being run with real money -- including
the bot's quirks (SMA-seeded EMA, Wilder smoothing details, a volume MA that
excludes the forming candle).
"""

import math


def sma(candles, period):
    if len(candles) < period:
        return 0.0
    return sum(c["close"] for c in candles[-period:]) / period


def ema(candles, period):
    """SMA-seeded EMA over the FIRST `period` candles, matching CalculateEMA."""
    if len(candles) < period:
        return 0.0
    cur = sum(c["close"] for c in candles[:period]) / period
    mult = 2.0 / (period + 1.0)
    for i in range(period, len(candles)):
        cur = (candles[i]["close"] - cur) * mult + cur
    return cur


def rsi(candles, period=14):
    if len(candles) < period + 1:
        return 50.0
    gains = losses = 0.0
    for i in range(1, period + 1):
        ch = candles[i]["close"] - candles[i - 1]["close"]
        if ch > 0:
            gains += ch
        else:
            losses -= ch
    avg_gain, avg_loss = gains / period, losses / period
    for i in range(period + 1, len(candles)):
        ch = candles[i]["close"] - candles[i - 1]["close"]
        g, l = (ch, 0.0) if ch > 0 else (0.0, -ch)
        avg_gain = (avg_gain * (period - 1) + g) / period
        avg_loss = (avg_loss * (period - 1) + l) / period
    if avg_loss == 0:
        return 100.0
    return 100.0 - (100.0 / (1.0 + avg_gain / avg_loss))


def _tr(c, prev):
    return max(c["high"] - c["low"],
               abs(c["high"] - prev["close"]),
               abs(c["low"] - prev["close"]))


def atr(candles, period=14):
    if len(candles) < period + 1:
        return 0.0
    tr_sum = sum(_tr(candles[i], candles[i - 1]) for i in range(1, period + 1))
    cur = tr_sum / period
    for i in range(period + 1, len(candles)):
        cur = (cur * (period - 1) + _tr(candles[i], candles[i - 1])) / period
    return cur


def adx(candles, period=14):
    n = len(candles)
    if n < period * 2:
        return 0.0
    tr = [0.0] * n
    pdm = [0.0] * n
    mdm = [0.0] * n
    for i in range(1, n):
        up = candles[i]["high"] - candles[i - 1]["high"]
        dn = candles[i - 1]["low"] - candles[i]["low"]
        if up > dn and up > 0:
            pdm[i] = up
        if dn > up and dn > 0:
            mdm[i] = dn
        tr[i] = _tr(candles[i], candles[i - 1])

    s_tr = [0.0] * n
    s_p = [0.0] * n
    s_m = [0.0] * n
    s_tr[period] = sum(tr[1:period + 1])
    s_p[period] = sum(pdm[1:period + 1])
    s_m[period] = sum(mdm[1:period + 1])
    for i in range(period + 1, n):
        s_tr[i] = s_tr[i - 1] - s_tr[i - 1] / period + tr[i]
        s_p[i] = s_p[i - 1] - s_p[i - 1] / period + pdm[i]
        s_m[i] = s_m[i - 1] - s_m[i - 1] / period + mdm[i]

    dx = [0.0] * n
    for i in range(period, n):
        if s_tr[i] == 0:
            continue
        dip = s_p[i] / s_tr[i] * 100
        dim = s_m[i] / s_tr[i] * 100
        tot = dip + dim
        if tot != 0:
            dx[i] = abs(dip - dim) / tot * 100

    cur = sum(dx[period:period * 2]) / period
    for i in range(period * 2, n):
        cur = (cur * (period - 1) + dx[i]) / period
    return cur


def williams_r(candles, period=14):
    if len(candles) < period:
        return -50.0
    window = candles[-period:]
    hh = max(c["high"] for c in window)
    ll = min(c["low"] for c in window)
    if hh == ll:
        return -50.0
    return (hh - candles[-1]["close"]) / (hh - ll) * -100.0


def vwap(candles, period=20):
    if len(candles) < period:
        return 0.0
    window = candles[-period:]
    pv = sum((c["high"] + c["low"] + c["close"]) / 3 * c["volume"] for c in window)
    vol = sum(c["volume"] for c in window)
    return pv / vol if vol else 0.0


def bollinger(candles, period=20, k=2.0):
    if len(candles) < period:
        return (0.0, 0.0, 0.0, 0.0)
    closes = [c["close"] for c in candles[-period:]]
    basis = sum(closes) / period
    sd = math.sqrt(sum((c - basis) ** 2 for c in closes) / period)
    upper, lower = basis + k * sd, basis - k * sd
    width = (upper - lower) / basis * 100.0 if basis else 0.0
    return (upper, basis, lower, width)


def volume_ma(candles, period=20):
    """Mean volume over the last `period` FULLY CLOSED candles, excluding the
    current one -- matches CalculateVolumeMA."""
    if len(candles) < period + 1:
        return 0.0
    return sum(c["volume"] for c in candles[-period - 1:-1]) / period


def volume_profile(candles, lookback=30, bins=50):
    """POC / VAH / VAL over a 70% value area. Port of ComputeVolumeProfile."""
    if not candles:
        return None
    active = candles[-lookback:] if len(candles) > lookback else candles
    if not active:
        return None
    lo = min(c["low"] for c in active)
    hi = max(c["high"] for c in active)
    total = sum(c["volume"] for c in active)
    if hi == lo:
        return {"poc": lo, "vah": lo, "val": lo, "total": total}

    size = (hi - lo) / bins
    b = [0.0] * bins
    for c in active:
        lob = min(max(int((c["low"] - lo) / size), 0), bins - 1)
        hib = min(max(int((c["high"] - lo) / size), 0), bins - 1)
        per = c["volume"] / (hib - lob + 1)
        for i in range(lob, hib + 1):
            b[i] += per

    poc_bin = max(range(bins), key=lambda i: b[i])
    target = total * 0.70
    cur = b[poc_bin]
    li = hi_i = poc_bin
    while cur < target and (li > 0 or hi_i < bins - 1):
        below = b[li - 1] if li > 0 else 0.0
        above = b[hi_i + 1] if hi_i < bins - 1 else 0.0
        if below > above:
            li -= 1
            cur += below
        elif above > below:
            hi_i += 1
            cur += above
        else:
            if li > 0:
                li -= 1
                cur += b[li]
            if hi_i < bins - 1:
                hi_i += 1
                cur += b[hi_i]
    return {"poc": lo + (poc_bin + 0.5) * size,
            "val": lo + li * size,
            "vah": lo + (hi_i + 1) * size,
            "total": total}


def consolidation(candles1d, min_days=21, max_range_pct=5.0):
    if len(candles1d) < min_days:
        return None
    active = candles1d[-min_days:]
    hh = max(c["high"] for c in active)
    ll = min(c["low"] for c in active)
    mid = (hh + ll) / 2.0
    rng = ((hh - ll) / mid * 100.0) if mid > 0 else 0.0
    return {"is_consolidating": rng <= max_range_pct,
            "high": hh, "low": ll, "range_pct": rng}


def gain_7d(candles1d):
    if len(candles1d) < 8:
        return 0.0
    old = candles1d[-8]["close"]
    if old == 0:
        return 0.0
    return (candles1d[-1]["close"] - old) / old * 100.0


def classify_regime(adx14):
    if adx14 > 25:
        return "TRENDING"
    if adx14 < 20:
        return "RANGING"
    return "MIXED"
