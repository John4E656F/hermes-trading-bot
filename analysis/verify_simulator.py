#!/usr/bin/env python3
"""
Verify the trade simulator on synthetic price paths where the correct answer
is known by hand. A backtest engine that mis-fills is worse than no backtest.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import backtest as bt
import strategy as sg

BAR = bt.BAR_MS
FAILS = []


def check(name, cond, detail=""):
    print(f"  {'PASS' if cond else 'FAIL'}  {name}{('  — ' + detail) if detail else ''}")
    if not cond:
        FAILS.append(name)


def make(prices, start=1_700_000_000_000, vol=1000.0):
    """prices: list of (open, high, low, close)."""
    return [{"ts": start + i * BAR, "open": o, "high": h, "low": l,
             "close": c, "volume": vol} for i, (o, h, l, c) in enumerate(prices)]


def fake_data(c4h):
    return {"symbol": "TEST", "c4h": c4h, "c1d": [], "funding": [], "oi": {}, "oi_list": []}


def state(price, atr, adx, ema20=None):
    return {"price": price, "atr14": atr, "daily_adx": adx,
            "ema20": ema20 if ema20 is not None else price, "regime": "MIXED"}


print("Simulator verification on synthetic paths\n")

# ATR=10, price=1000, ADX=30 -> slMult 2.5 (SL dist 25), tpMult 6.25 (TP dist 62.5)
ATR, PRICE, ADX = 10.0, 1000.0, 30.0
SL_DIST, TP_DIST = ATR * 2.5, ATR * 6.25

# ── 1. Long hits take-profit ─────────────────────────────────────────────
flat = [(PRICE, PRICE, PRICE, PRICE)]
up = flat + [(1000, 1000 + TP_DIST + 5, 999, 1000 + TP_DIST + 5)]
d = fake_data(make(up))
t = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("long take-profit fires", t is not None and t["exit_reason"] == "TP_HIT",
      f"reason={t['exit_reason'] if t else None}")
check("long TP result is positive in R", t and t["result_r"] > 0, f"R={t['result_r']:+.3f}" if t else "")

# ── 2. Long hits stop-loss ───────────────────────────────────────────────
down = flat + [(1000, 1001, 1000 - SL_DIST - 5, 1000 - SL_DIST - 5)]
d = fake_data(make(down))
t = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("long stop-loss fires", t is not None and t["exit_reason"] == "SL_HIT",
      f"reason={t['exit_reason'] if t else None}")
check("long SL loses about 1R", t and -1.15 < t["result_r"] < -0.95,
      f"R={t['result_r']:+.3f} (slightly worse than -1R after fees/slippage)" if t else "")

# ── 3. A bar containing BOTH levels must resolve as the STOP ─────────────
both = flat + [(1000, 1000 + TP_DIST + 5, 1000 - SL_DIST - 5, 1000)]
d = fake_data(make(both))
t = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("ambiguous bar resolves as the stop, not the target",
      t is not None and t["exit_reason"] == "SL_HIT",
      f"reason={t['exit_reason'] if t else None}")

# ── 4. Short is the mirror image ─────────────────────────────────────────
d = fake_data(make(down))
t = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.SELL, "test", 3, 0.85)
check("short profits when price falls", t is not None and t["result_r"] > 0,
      f"reason={t['exit_reason']} R={t['result_r']:+.3f}" if t else "")

# ── 5. Costs are always charged against the trade ────────────────────────
d = fake_data(make(up))
t = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("fees are non-zero and reduce the result", t and t["fees"] > 0 and t["net_pnl"] < t["gross_pnl"],
      f"gross={t['gross_pnl']:.2f} fees={t['fees']:.2f} net={t['net_pnl']:.2f}" if t else "")
check("slippage worsens the entry for a long", t and t["entry_price"] > PRICE,
      f"entry={t['entry_price']:.4f} vs signal close {PRICE}" if t else "")

# ── 6. The fee gate rejects a stop inside 3x round-trip friction ─────────
tiny = fake_data(make(flat * 3))
t = bt.simulate_trade(tiny, {0: state(PRICE, 0.10, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("fee gate rejects a stop that is too tight", t is None,
      "ATR 0.10 -> SL dist 0.25 vs 3x friction ~6.3")

# ── 7. No look-ahead: the signal bar itself can never trigger an exit ────
trap = [(1000, 1000 + TP_DIST + 50, 1000 - SL_DIST - 50, 1000)] + flat * 3
d = fake_data(make(trap))
t = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("signal bar's own range cannot exit the trade",
      t is not None and t["exit_reason"] == "TIMEOUT",
      f"reason={t['exit_reason'] if t else None}")

# ── 8. Funding is charged to longs and credited to shorts ───────────────
c = make(flat * 5)
d = fake_data(c)
d["funding"] = [{"ts": c[0]["ts"] + BAR // 2, "rate": 0.001},
                {"ts": c[1]["ts"] + BAR // 2, "rate": 0.001}]
tl = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
ts_ = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.SELL, "test", 3, 0.85)
check("positive funding costs the long", tl and tl["funding_cost"] > 0,
      f"{tl['funding_cost']:+.4f}" if tl else "")
check("positive funding credits the short", ts_ and ts_["funding_cost"] < 0,
      f"{ts_['funding_cost']:+.4f}" if ts_ else "")

# ── 9. Funding outside the published window is imputed and flagged ──────
d2 = fake_data(make(flat * 5))
d2["funding"] = [{"ts": 1_900_000_000_000, "rate": 0.0005}]
t = bt.simulate_trade(d2, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("funding outside the published window is flagged as imputed",
      t is not None and t["funding_imputed"] is True)

# ── 10. MAE/MFE are measured in R ────────────────────────────────────────
path = flat + [(1000, 1000 + TP_DIST * 0.4, 1000 - SL_DIST * 0.6, 1000)] + \
    [(1000, 1000 + TP_DIST + 5, 999, 1000 + TP_DIST + 5)]
d = fake_data(make(path))
t = bt.simulate_trade(d, {0: state(PRICE, ATR, ADX)}, 0, sg.BUY, "test", 3, 0.85)
check("MAE is recorded before the winning exit", t and 0.4 < t["mae_r"] < 0.8,
      f"MAE={t['mae_r']:.3f}R (drawdown was 0.6x the stop distance)" if t else "")
check("MFE is at least the realised result", t and t["mfe_r"] >= t["result_r"],
      f"MFE={t['mfe_r']:.3f}R result={t['result_r']:.3f}R" if t else "")

print(f"\n{'All simulator checks passed.' if not FAILS else 'FAILURES: ' + ', '.join(FAILS)}")
sys.exit(1 if FAILS else 0)
