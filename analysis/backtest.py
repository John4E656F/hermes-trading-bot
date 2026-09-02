#!/usr/bin/env python3
"""
Walk-forward backtest of Hermes' strategies.

WHAT IS MODELLED
  entry     close of the signal bar, plus slippage (no same-bar lookahead)
  exit      ATR bracket (SL/TP) with the executor's ADX-dependent multipliers,
            plus the EMA20-distance trailing stop that activates at the
            entry->TP midpoint, exactly as executor.go sets it
  fees      TAKER_FEE_RATE (0.055%) per side on notional, both sides
  funding   every 8h settlement inside the hold window, at the venue's
            realised rate, signed by position side
  slippage  SLIPPAGE_BPS per side (the bot's own fee gate assumes 10bps
            round-trip, so 5bps per side is its own working assumption)

CONSERVATIVE ASSUMPTIONS
  - When a bar's range contains BOTH the stop and the target, the STOP is
    assumed to fill first. Intrabar sequence is unknowable from OHLCV, and
    the optimistic assumption is how backtests manufacture edges that do not
    survive contact with a live book.
  - Entries fill at the close of the signal bar. The live bot places a LIMIT
    order at the current price, which may never fill; assuming it always
    fills is generous to the strategy, and is noted as such.

WALK-FORWARD
  Every value at bar i is computed from candles[0..i] only. There is no
  parameter fitting in this script -- the strategy parameters are read from
  the shipped code -- so the entire history is one out-of-sample window.
"""

import collections
import json
import math
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import hermes_data as hd
import strategy as sg

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TAKER_FEE_RATE = 0.00055   # matches executor.go
SLIPPAGE_BPS = 5.0         # per side
RISK_PCT_FLAT = 0.0050     # flat sizing, so modes are comparable
START_EQUITY = 10_000.0
MAX_HOLD_BARS = 90         # 15 days on 4H bars; abandon a bracket that never fills
WARMUP_4H = 60
MAX_CONCURRENT = 5         # matches the live position-count limit
BAR_MS = 4 * 3600 * 1000


# ─── Data assembly ───────────────────────────────────────────────────────

def load_symbol(symbol, max_bars=6000):
    c4h = hd.fetch_candles(symbol, "4H", max_bars=max_bars)
    if len(c4h) < WARMUP_4H + 30:
        return None
    c1d = hd.fetch_candles(symbol, "1D", max_bars=1200)
    if len(c1d) < 40:
        return None
    funding = hd.fetch_funding_history(symbol)
    oi = hd.fetch_oi_history(symbol, "4H")
    return {"symbol": symbol, "c4h": c4h, "c1d": c1d,
            "funding": funding, "oi": {r["ts"]: r["oi"] for r in oi},
            "oi_list": oi}


def funding_at(funding, ts):
    """Most recent settled funding rate at or before ts."""
    lo, hi, found = 0, len(funding) - 1, None
    while lo <= hi:
        mid = (lo + hi) // 2
        if funding[mid]["ts"] <= ts:
            found = funding[mid]["rate"]
            lo = mid + 1
        else:
            hi = mid - 1
    return found


def funding_between(funding, t0, t1, side):
    """
    Total funding COST over a hold window, as a fraction of notional.
    Longs pay a positive rate; shorts receive it.
    """
    total = 0.0
    for f in funding:
        if t0 < f["ts"] <= t1:
            total += f["rate"] if side == sg.BUY else -f["rate"]
    return total


def build_states(data):
    """Per-bar signal state for one symbol. Strictly causal."""
    c4h, c1d = data["c4h"], data["c1d"]
    oi_list = data["oi_list"]
    oi_by_ts = data["oi"]
    states = {}

    di = 0
    for i in range(WARMUP_4H, len(c4h)):
        ts = c4h[i]["ts"]
        # Daily candles CLOSED at or before this 4H bar close.
        while di < len(c1d) and c1d[di]["ts"] + 86_400_000 <= ts:
            di += 1
        if di < 30:
            continue
        window1d = c1d[:di]

        fr = funding_at(data["funding"], ts) if data["funding"] else None

        oi_cur = oi_by_ts.get(ts)
        oi_chg = 0.0
        if oi_cur:
            prev = oi_by_ts.get(ts - 6 * BAR_MS)  # 6 x 4H = 24h, matches pastIdx=6
            if prev and prev > 0:
                oi_chg = (oi_cur - prev) / prev * 100.0
            else:
                oi_cur = None  # no comparable point: S2 must not fire on a guess

        states[i] = sg.compute_bar_state(c4h[:i + 1], window1d, fr, oi_chg, oi_cur)
    return states


# ─── Trade simulation ────────────────────────────────────────────────────

def bracket(price, atr14, daily_adx, action):
    """Port of executor.go's ADX-aware SL/TP construction."""
    if daily_adx < 25:
        sl_mult, tp_mult = 3.0, 7.5
    elif daily_adx < 40:
        sl_mult, tp_mult = 2.5, 6.25
    else:
        sl_mult, tp_mult = 2.0, 5.0
    dist = atr14 * sl_mult
    if dist <= 0:
        return None
    if action == sg.BUY:
        return {"sl": price - dist, "tp": price + atr14 * tp_mult, "risk_per_unit": dist}
    return {"sl": price + dist, "tp": price - atr14 * tp_mult, "risk_per_unit": dist}


def fee_gate_passes(price, atr14, daily_adx):
    """executor.go refuses trades whose stop is inside 3x round-trip friction."""
    b = bracket(price, atr14, daily_adx, sg.BUY)
    if not b:
        return False
    friction = price * (TAKER_FEE_RATE * 2 + 0.001)
    return b["risk_per_unit"] >= friction * 3


def simulate_trade(data, states, i, action, strategy_label, conviction, confidence):
    """
    Open at bar i's close and walk forward until an exit. Returns a trade dict
    or None when the setup is not tradeable.
    """
    c4h = data["c4h"]
    st = states[i]
    atr14, adx_d = st["atr14"], st["daily_adx"]
    raw_price = st["price"]
    if atr14 <= 0 or raw_price <= 0:
        return None
    if not fee_gate_passes(raw_price, atr14, adx_d):
        return None

    br = bracket(raw_price, atr14, adx_d, action)
    if not br:
        return None

    slip = SLIPPAGE_BPS / 10_000.0
    entry = raw_price * (1 + slip) if action == sg.BUY else raw_price * (1 - slip)
    risk_per_unit = br["risk_per_unit"]
    sl, tp = br["sl"], br["tp"]

    # Trailing stop: distance = |price - EMA20|, arms at the entry->TP midpoint.
    ema20 = st["ema20"]
    trail_dist = abs(raw_price - ema20) if ema20 > 0 else 0.0
    activation = raw_price + (tp - raw_price) * 0.5 if action == sg.BUY else \
        raw_price - (raw_price - tp) * 0.5

    trail_armed = False
    trail_stop = None
    mae = mfe = 0.0
    exit_price = exit_ts = None
    exit_reason = "TIMEOUT"

    for j in range(i + 1, min(i + 1 + MAX_HOLD_BARS, len(c4h))):
        bar = c4h[j]

        if action == sg.BUY:
            mae = max(mae, entry - bar["low"])
            mfe = max(mfe, bar["high"] - entry)
        else:
            mae = max(mae, bar["high"] - entry)
            mfe = max(mfe, entry - bar["low"])

        # Conservative ordering: the adverse level is tested first.
        hit_sl = bar["low"] <= sl if action == sg.BUY else bar["high"] >= sl
        if trail_armed and trail_stop is not None:
            hit_trail = bar["low"] <= trail_stop if action == sg.BUY else bar["high"] >= trail_stop
        else:
            hit_trail = False
        hit_tp = bar["high"] >= tp if action == sg.BUY else bar["low"] <= tp

        if hit_sl:
            exit_price, exit_ts, exit_reason = sl, bar["ts"], "SL_HIT"
            break
        if hit_trail:
            exit_price, exit_ts, exit_reason = trail_stop, bar["ts"], "TRAIL_STOP"
            break
        if hit_tp:
            exit_price, exit_ts, exit_reason = tp, bar["ts"], "TP_HIT"
            break

        # Arm / advance the trailing stop after the bar resolves.
        if not trail_armed and trail_dist > 0:
            if (action == sg.BUY and bar["high"] >= activation) or \
               (action == sg.SELL and bar["low"] <= activation):
                trail_armed = True
                trail_stop = (bar["close"] - trail_dist) if action == sg.BUY \
                    else (bar["close"] + trail_dist)
        elif trail_armed:
            cand = (bar["close"] - trail_dist) if action == sg.BUY else (bar["close"] + trail_dist)
            trail_stop = max(trail_stop, cand) if action == sg.BUY else min(trail_stop, cand)

    if exit_price is None:
        j = min(i + MAX_HOLD_BARS, len(c4h) - 1)
        if j <= i:
            return None
        exit_price, exit_ts = c4h[j]["close"], c4h[j]["ts"]

    exit_fill = exit_price * (1 - slip) if action == sg.BUY else exit_price * (1 + slip)

    # Per-unit result, then scaled to a fixed fractional risk.
    gross_per_unit = (exit_fill - entry) if action == sg.BUY else (entry - exit_fill)
    qty = (START_EQUITY * RISK_PCT_FLAT) / risk_per_unit
    notional_in = entry * qty
    notional_out = exit_fill * qty

    gross = gross_per_unit * qty
    fees = (notional_in + notional_out) * TAKER_FEE_RATE
    fund_frac = funding_between(data["funding"], c4h[i]["ts"], exit_ts, action)
    funding_cost = fund_frac * notional_in
    net = gross - fees - funding_cost
    initial_risk = START_EQUITY * RISK_PCT_FLAT

    return {
        "symbol": data["symbol"], "side": action, "strategy": strategy_label,
        "conviction": conviction, "confidence": confidence,
        "entry_ts": c4h[i]["ts"], "exit_ts": exit_ts,
        "entry_price": entry, "exit_price": exit_fill,
        "quantity": qty, "notional": notional_in,
        "gross_pnl": gross, "fees": fees, "funding_cost": funding_cost,
        "slippage": (notional_in + notional_out) * slip, "net_pnl": net,
        "initial_risk_usd": initial_risk,
        "result_r": net / initial_risk,
        "gross_r": gross / initial_risk,
        "mae_r": mae * qty / initial_risk,
        "mfe_r": mfe * qty / initial_risk,
        "exit_reason": exit_reason,
        "regime": states[i]["regime"],
        "bars_held": max(1, (exit_ts - c4h[i]["ts"]) // BAR_MS),
    }


# ─── Modes ───────────────────────────────────────────────────────────────

def run_single_strategy(universe, states_by_symbol, key):
    """S1/S2/S3 in isolation: trade the lens' own call, nothing else."""
    trades = []
    for sym, data in universe.items():
        states = states_by_symbol[sym]
        open_until = -1
        for i in sorted(states):
            if i <= open_until:
                continue
            sub = states[i][key]
            if not sub["active"]:
                continue
            t = simulate_trade(data, states, i, sub["action"],
                               key.upper(), 1, 0.0)
            if t:
                trades.append(t)
                open_until = i + int(t["bars_held"])
    return trades


def run_blended(universe, states_by_symbol, tiered_sizing=False):
    """
    The shipped configuration: master chain + conviction stacking, with the
    live gates — conviction >= 2, confidence >= 0.70, one position per symbol,
    max 5 concurrent, top-3 candidates per cycle, one signal per strategy per
    cycle. AI layers are excluded (see strategy.py).
    """
    all_bars = sorted({ts for d in universe.values() for ts in [c["ts"] for c in d["c4h"]]})
    idx_by_ts = {sym: {d["c4h"][i]["ts"]: i for i in states_by_symbol[sym]}
                 for sym, d in universe.items()}

    trades = []
    busy_until = collections.defaultdict(lambda: -1)  # symbol -> exit ts

    for ts in all_bars:
        open_now = sum(1 for s, until in busy_until.items() if until > ts)
        if open_now >= MAX_CONCURRENT:
            continue

        candidates = []
        for sym, data in universe.items():
            if busy_until[sym] > ts:
                continue
            i = idx_by_ts[sym].get(ts)
            if i is None:
                continue
            st = states_by_symbol[sym][i]
            sig = sg.master_signal(st)
            if sig["action"] == sg.HOLD:
                continue
            if sig["conviction"] < 2 or sig["confidence"] < 0.70:
                continue
            candidates.append((sym, data, i, st, sig))

        if not candidates:
            continue

        # Rank: conviction desc, then |7d gain| desc (main.go's final sort).
        candidates.sort(key=lambda c: (-c[4]["conviction"], -abs(c[3]["gain7d"])))
        candidates = candidates[:3]

        # One signal per (strategy, direction) per cycle.
        seen = set()
        for sym, data, i, st, sig in candidates:
            if open_now >= MAX_CONCURRENT:
                break
            base = sig["strategy"]
            for p in ("META: ", "CONFIRMED: ", "QUALITY: "):
                if base.startswith(p):
                    base = base[len(p):]
            base = base.replace(" + S4", "")
            key = (base, sig["action"])
            if key in seen:
                continue
            seen.add(key)

            t = simulate_trade(data, states_by_symbol[sym], i, sig["action"],
                               sig["strategy"], sig["conviction"], sig["confidence"])
            if not t:
                continue
            if tiered_sizing:
                conf = sig["confidence"]
                mult = (0.0075 if conf >= 0.85 else 0.0050 if conf >= 0.75 else 0.0035) / RISK_PCT_FLAT
                for f in ("gross_pnl", "fees", "funding_cost", "slippage",
                          "net_pnl", "quantity", "notional", "initial_risk_usd"):
                    t[f] *= mult
            trades.append(t)
            busy_until[sym] = t["exit_ts"]
            open_now += 1

    return trades


# ─── Metrics ─────────────────────────────────────────────────────────────

def dedup_trades(trades):
    """Episode dedup (Step 3) applied to the backtest: one trade per
    (symbol, side, entry bar). Overlapping re-entries on the same bar are the
    same opportunity, not independent ones."""
    seen, out = set(), []
    for t in sorted(trades, key=lambda t: t["entry_ts"]):
        k = (t["symbol"], t["side"], t["entry_ts"])
        if k in seen:
            continue
        seen.add(k)
        out.append(t)
    return out


def metrics(trades, label):
    n = len(trades)
    if n == 0:
        return {"label": label, "n": 0}

    trades = sorted(trades, key=lambda t: t["exit_ts"])
    rs = [t["result_r"] for t in trades]
    wins = [r for r in rs if r > 0]
    losses = [r for r in rs if r <= 0]

    gross_win = sum(wins)
    gross_loss = abs(sum(losses))
    pf = (gross_win / gross_loss) if gross_loss > 0 else (float("inf") if gross_win > 0 else 0.0)

    # Equity curve at fixed fractional risk.
    eq, peak, max_dd = START_EQUITY, START_EQUITY, 0.0
    curve = []
    for t in trades:
        eq += t["net_pnl"]
        peak = max(peak, eq)
        max_dd = max(max_dd, (peak - eq) / peak * 100.0)
        curve.append(eq)

    mean_r = sum(rs) / n
    var = sum((r - mean_r) ** 2 for r in rs) / n
    sd = math.sqrt(var)
    downside = [min(0.0, r) for r in rs]
    dsd = math.sqrt(sum(d * d for d in downside) / n)

    # Per-trade Sharpe/Sortino annualised by observed trade frequency.
    span_days = (trades[-1]["exit_ts"] - trades[0]["entry_ts"]) / 86_400_000 or 1
    per_year = n / span_days * 365.0
    sharpe = (mean_r / sd * math.sqrt(per_year)) if sd > 0 else None
    sortino = (mean_r / dsd * math.sqrt(per_year)) if dsd > 0 else None

    longs = [t["result_r"] for t in trades if t["side"] == sg.BUY]
    shorts = [t["result_r"] for t in trades if t["side"] == sg.SELL]

    return {
        "label": label, "n": n,
        "net_return_pct": (eq - START_EQUITY) / START_EQUITY * 100.0,
        "net_pnl": eq - START_EQUITY,
        "max_drawdown_pct": max_dd,
        "win_rate": len(wins) / n * 100.0,
        "avg_winner_r": (sum(wins) / len(wins)) if wins else 0.0,
        "avg_loser_r": (sum(losses) / len(losses)) if losses else 0.0,
        "expectancy_r": mean_r,
        "profit_factor": pf,
        "sharpe": sharpe, "sortino": sortino,
        "long_n": len(longs), "short_n": len(shorts),
        "long_expectancy_r": (sum(longs) / len(longs)) if longs else None,
        "short_expectancy_r": (sum(shorts) / len(shorts)) if shorts else None,
        "total_fees": sum(t["fees"] for t in trades),
        "total_funding": sum(t["funding_cost"] for t in trades),
        "total_slippage": sum(t["slippage"] for t in trades),
        "gross_expectancy_r": sum(t["gross_r"] for t in trades) / n,
        "avg_mae_r": sum(t["mae_r"] for t in trades) / n,
        "avg_mfe_r": sum(t["mfe_r"] for t in trades) / n,
        "exit_reasons": dict(collections.Counter(t["exit_reason"] for t in trades)),
        "span_days": span_days,
    }


def fmt(m):
    if m["n"] == 0:
        return f"{m['label']:<26} {'no trades generated':>60}"
    sh = f"{m['sharpe']:.2f}" if m["sharpe"] is not None else "n/a"
    so = f"{m['sortino']:.2f}" if m["sortino"] is not None else "n/a"
    pf = "inf" if m["profit_factor"] == float("inf") else f"{m['profit_factor']:.2f}"
    le = f"{m['long_expectancy_r']:+.3f}" if m["long_expectancy_r"] is not None else "  n/a"
    se = f"{m['short_expectancy_r']:+.3f}" if m["short_expectancy_r"] is not None else "  n/a"
    return (f"{m['label']:<26} {m['n']:>5} {m['net_return_pct']:>8.2f}% "
            f"{m['max_drawdown_pct']:>7.2f}% {m['win_rate']:>6.1f}% "
            f"{m['avg_winner_r']:>7.2f} {m['avg_loser_r']:>7.2f} "
            f"{m['expectancy_r']:>+8.4f} {pf:>6} {sh:>7} {so:>7} {le:>8} {se:>8}")


HEADER = (f"{'Configuration':<26} {'n':>5} {'net ret':>9} {'maxDD':>8} {'win':>7} "
          f"{'avgW R':>7} {'avgL R':>7} {'exp R':>9} {'PF':>6} {'Sharpe':>7} "
          f"{'Sortino':>7} {'longR':>8} {'shortR':>8}")
