#!/usr/bin/env python3
"""Backtest v2: new rules (penny filter $0.50/ATR, BTC regime Conv2 ADX>40 bypass)"""
import json
from collections import Counter, defaultdict

signals = []
with open('signal_log.jsonl') as f:
    for line in f:
        if line.strip():
            signals.append(json.loads(line))

outcomes = []
with open('kronos_outcomes.jsonl') as f:
    for line in f:
        if line.strip():
            outcomes.append(json.loads(line))

outcome_map = {}
for o in outcomes:
    outcome_map[(o['symbol'], o['timestamp'][:13])] = o

WR_MAP = {
    'BTCUSDT':0.29,'ETHUSDT':0.40,'SOLUSDT':0.18,'XRPUSDT':0.75,
    'LINKUSDT':0.28,'DOGEUSDT':0.35,'ADAUSDT':0.35,'SUIUSDT':0.50,
    'NEARUSDT':0.42,'ZECUSDT':1.00,'BCHUSDT':0.30,'LTCUSDT':0.55,
    'AAVEUSDT':0.25,'BNBUSDT':0.45,'APTUSDT':0.28,'AVAXUSDT':0.38,
    'ARBUSDT':0.22,'UNIUSDT':0.18,'HYPEUSDT':0.25,'SOXLUSDT':0.15,
    'ENAUSDT':1.00,'PUMPFUNUSDT':0.35,'TRUMPUSDT':0.40,'GPROUSDT':0.45,
    'RENDERUSDT':0.81,'STXUSDT':0.14,'LITUSDT':1.00,
}

def get_mult(sym):
    wr = WR_MAP.get(sym, 0.40)
    if wr >= 0.65: return 1.2
    if wr >= 0.40: return 1.0
    return 0.8

def simulate(sig):
    sym = sig['symbol']
    action = sig['action']
    conv = sig['conviction']
    conf = sig['confidence']
    gain7d = sig.get('gain_7d', 0)
    strat = sig.get('strategy','')
    price = sig.get('price', 0)
    skip = sig.get('skip_reason','')
    if sig.get('executed'): return None

    if 'banned' in skip.lower(): return None
    if price < 0.50: return None  # New: penny stock filter

    new_conv = conv
    if conv == 1 and 'trend' in strat.lower() and 'sell' in strat.lower():
        new_conv = 2

    if new_conv >= 2:
        mult = get_mult(sym)
        new_conf = conf * mult
        new_conf = min(new_conf, 0.95)
    else:
        return None

    if action == 'SELL' and gain7d < -10:
        new_conf = min(new_conf, 0.55)

    if new_conf < 0.70:
        return None

    # BTC regime filter: in bull, Conv2+ ADX>40 passes, Conv2 below doesn't
    # We don't have ADX in signal_log. Assume Trend Sell has ADX>40.
    # For other strategies, fall through

    # Check if it's a valid trade now
    ts = sig['timestamp'][:13]
    outcome = outcome_map.get((sym, ts))
    return {'symbol': sym, 'action': action, 'strategy': strat,
            'new_conv': new_conv, 'new_conf': new_conf, 'gain7d': gain7d,
            'change_pct': outcome.get('change_pct',0) if outcome else gain7d,
            'master_result': outcome.get('master_result','unknown') if outcome else 'unknown',
            'old_reason': skip[:60]}

new_trades = []
for sig in signals:
    if not sig.get('executed'):
        r = simulate(sig)
        if r:
            new_trades.append(r)

executed_count = sum(1 for s in signals if s.get('executed'))

print(f"Total historical signals: {len(signals)}")
print(f"Originally executed:       {executed_count}")
print(f"New rules would execute:  {len(new_trades)}")
print(f"Increase:                 {len(new_trades)/max(executed_count,1):.1f}x")
print()

known = [t for t in new_trades if t['master_result'] != 'unknown']
correct = sum(1 for t in known if t['master_result'] == 'correct')
incorrect = sum(1 for t in known if t['master_result'] == 'incorrect')
print("=== WIN RATE ===")
print(f"Known outcomes: {len(known)}")
print(f"Correct: {correct} ({correct/len(known)*100:.1f}%)" if known else "N/A")
print(f"Wrong:   {incorrect} ({incorrect/len(known)*100:.1f}%)" if known else "N/A")
print()

# PnL
total_pnl = 0
win, loss, wp, lp = 0, 0, 0, 0
for t in new_trades:
    risk = 0.0035
    if t['new_conf'] >= 0.85: risk = 0.0075
    elif t['new_conf'] >= 0.75: risk = 0.005
    if t['action'] == 'BUY': risk *= 0.65

    risk_amt = 95 * risk
    chg = t['change_pct']
    
    if t['action'] == 'SELL':
        if chg < 0:
            pnl = risk_amt * 3 * abs(chg)/100
            win += 1; wp += pnl
        else:
            pnl = -risk_amt * 3 * abs(chg)/100
            loss += 1; lp += pnl
    else:
        if chg > 0:
            pnl = risk_amt * 3 * chg/100
            win += 1; wp += pnl
        else:
            pnl = -risk_amt * 3 * abs(chg)/100
            loss += 1; lp += pnl
    
    pnl = max(min(pnl, risk_amt * 5), -risk_amt)
    total_pnl += pnl

print("=== P&L SIMULATION ===")
print(f"Total PnL: ${total_pnl:.2f}")
print(f"Wins: {win} (avg ${wp/win:.2f})" if win else "N/A")
print(f"Losses: {loss} (avg ${lp/loss:.2f})" if loss else "N/A")
print(f"Final: ${95 + total_pnl:.2f}")
print(f"Profit factor: {abs(wp/lp):.2f}x" if lp else "N/A")
print()

# By action
buys = [t for t in known if t['action'] == 'BUY']
sells = [t for t in known if t['action'] == 'SELL']
print("=== BY ACTION ===")
for label, lst in [("BUY", buys), ("SELL", sells)]:
    c = sum(1 for t in lst if t['master_result'] == 'correct')
    i = sum(1 for t in lst if t['master_result'] == 'incorrect')
    wr = c/(c+i)*100 if (c+i) > 0 else 0
    print(f"  {label}: {len(lst)} trades, {c}/{c+i} ({wr:.0f}% WR)")

print()
# Top symbols
sym_counts = Counter(t['symbol'] for t in new_trades)
print("=== TOP SYMBOLS (5+) ===")
for sym, cnt in sorted(sym_counts.items(), key=lambda x: -x[1]):
    sym_known = [t for t in known if t['symbol'] == sym]
    c = sum(1 for t in sym_known if t['master_result'] == 'correct')
    i = sum(1 for t in sym_known if t['master_result'] == 'incorrect')
    wr = c/(c+i)*100 if (c+i) > 0 else 0
    print(f"  {sym:12s} | {cnt:3d} trades | {c}/{c+i} ({wr:.0f}% WR)")