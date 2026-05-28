#!/usr/bin/env python3
"""Get current ticker prices for candidates."""
from pybit.unified_trading import HTTP
import json

session = HTTP(testnet=False, recv_window=60000)

symbols = ['FFUSDT', 'TRXUSDT', 'SAHARAUSDT', 'LABUSDT', 'HBARUSDT', 'SEIUSDT', 'HUSDT', 'XPLUSDT', 'ALGOUSDT', 'BSBUSDT', 'VVVUSDT', 'NEARUSDT', 'PLAYSOUTUSDT']

for sym in symbols:
    try:
        resp = session.get_tickers(category='linear', symbol=sym)
        if resp['retCode'] == 0:
            t = resp['result']['list'][0]
            print(f"{sym}: bid={t['bid1Price']} ask={t['ask1Price']} last={t['lastPrice']} high24h={t['highPrice24h']} low24h={t['lowPrice24h']}")
        else:
            print(f"{sym}: ERROR {resp}")
    except Exception as e:
        print(f"{sym}: EXCEPTION {e}")
