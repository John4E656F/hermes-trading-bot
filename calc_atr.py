import requests
import json

# Get kline data to calculate ATR for BNBUSDT and HYPEUSDT
pairs = ['BNBUSDT', 'HYPEUSDT']
for symbol in pairs:
    resp = requests.get(f'https://api.bybit.com/v5/market/kline?category=linear&symbol={symbol}&interval=D&limit=21')
    data = resp.json()
    if data['retCode'] == 0:
        candles = data['result']['list']
        # candles are in reverse chronological order [0]=most recent
        # Format: [timestamp, open, high, low, close, volume, turnover]
        highs = [float(c[2]) for c in candles]
        lows = [float(c[3]) for c in candles]
        closes = [float(c[4]) for c in candles]
        closes_prev = [float(c[4]) for c in candles[1:]] + [closes[-1]]
        
        atrs = []
        for i in range(1, len(candles)):
            h = highs[i]
            l_ = lows[i]
            pc = closes[i-1]
            tr = max(h-l_, abs(h-pc), abs(l_-pc))
            atrs.append(tr)
        
        atr_14 = sum(atrs[:14]) / 14
        atr_21 = sum(atrs[:21]) / 21
        
        current_price = closes[0]
        
        print(f"\n=== {symbol} ===")
        print(f"Current price: {current_price}")
        print(f"14-day ATR: {atr_14:.4f}")
        print(f"21-day ATR: {atr_21:.4f}")
        
        # ADX-based SL calculation
        adx_bnb = 26.58  # from scan for BNB
        adx_hype = 40.60
        
        if symbol == 'BNBUSDT':
            adx = 26.58
            if adx < 25:
                sl_mult = 4.0
            elif adx < 40:
                sl_mult = 2.5
            else:
                sl_mult = 2.0
            sl_distance = atr_14 * sl_mult
            
            entry = current_price
            stop_loss = entry - sl_distance
            take_profit = entry + (sl_distance * 1.8)  # 1.8:1 reward:risk
            
            risk_pct = sl_distance / entry * 100
            max_risk_usd = 94.01 * 0.015  # 1.5% of wallet
            position_size_usd = max_risk_usd / (sl_distance / entry)
            
            print(f"ADX: {adx} → SL multiplier: {sl_mult}x ATR")
            print(f"SL distance: ${sl_distance:.2f} ({risk_pct:.2f}%)")
            print(f"Entry: ${entry:.2f}")
            print(f"Stop Loss: ${stop_loss:.2f}")
            print(f"Take Profit: ${take_profit:.2f}")
            print(f"Max risk (1.5%): ${max_risk_usd:.2f}")
            print(f"Position size: ${position_size_usd:.2f}")
            print(f"Confidence: 75%")
            
    else:
        print(f"{symbol}: error {data}")