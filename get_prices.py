import requests

pairs = ['BNBUSDT', 'HYPEUSDT', 'PLAYSOUTUSDT', 'FFUSDT', 'BCHUSDT', 'HNTUSDT']
for symbol in pairs:
    resp = requests.get(f'https://api.bybit.com/v5/market/tickers?category=linear&symbol={symbol}')
    data = resp.json()
    if data['retCode'] == 0:
        t = data['result']['list'][0]
        print(f"{symbol}: last={t['lastPrice']} bid={t['bid1Price']} ask={t['ask1Price']}")
    else:
        print(f"{symbol}: error {data}")
