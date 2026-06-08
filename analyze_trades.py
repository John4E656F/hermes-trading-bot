#!/usr/bin/env python3
import json

with open('/home/hermes/hermes-trading-bot/trade_history.json') as f:
    data = json.load(f)

trades = data.get('trades', [])
open_trades = [t for t in trades if t.get('outcome') == 'PENDING']
closed_trades = [t for t in trades if t.get('outcome') in ('LOSS', 'WIN')]

print(f'Total trades recorded: {len(trades)}')
print(f'Open (pending): {len(open_trades)}')
print(f'Closed: {len(closed_trades)}')
wins = [t for t in closed_trades if t.get('outcome') == 'WIN']
losses = [t for t in closed_trades if t.get('outcome') == 'LOSS']
print(f'Wins: {len(wins)}, Losses: {len(losses)}')
total_pnl = sum(t.get('pnl', 0) for t in closed_trades)
print(f'Closed P&L: ${total_pnl:.2f}')

# Get latest wallet
for t in reversed(trades):
    w = t.get('wallet_at_time')
    if w:
        print(f'Latest wallet snapshot: ${w:.2f}')
        break

print()
print('=== OPEN POSITIONS ===')
for t in open_trades:
    print(f'  {t["symbol"]} {t["side"]} @ {t["entry_price"]} | SL: {t["stop_loss"]} TP: {t["take_profit"]} | Conf: {t["confidence_pct"]}% | Size: ${t.get("position_size_usd",0):.2f} | PnL: ${t.get("pnl",0):.2f}')

if not open_trades:
    print('  (none)')