package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type BybitClient struct {
	BaseURL    string
	APIKey     string
	APISecret  string
	HTTPClient *http.Client
}

func NewBybitClient() *BybitClient {
	return &BybitClient{
		BaseURL:    "https://api.bybit.com",
		APIKey:     os.Getenv("BYBIT_API_KEY"),
		APISecret:  os.Getenv("BYBIT_API_SECRET"),
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// generateSignature hashes parameters using HMAC-SHA256 required by Bybit private infrastructure
func (c *BybitClient) generateSignature(timestamp, recvWindow, body string) string {
	paramStr := timestamp + c.APIKey + recvWindow + body
	h := hmac.New(sha256.New, []byte(c.APISecret))
	h.Write([]byte(paramStr))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *BybitClient) FetchKlines(symbol, interval string, limit int) ([]Candle, error) {
	url := fmt.Sprintf("%s/v5/market/kline?category=linear&symbol=%s&interval=%s&limit=%d", c.BaseURL, symbol, interval, limit)
	resp, err := c.HTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List [][]string `json:"list"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if res.RetCode != 0 {
		return nil, fmt.Errorf("bybit api returned error: %s", res.RetMsg)
	}

	var candles []Candle
	for i := len(res.Result.List) - 1; i >= 0; i-- {
		item := res.Result.List[i]
		tsMs, _ := strconv.ParseInt(item[0], 10, 64)
		open, _ := strconv.ParseFloat(item[1], 64)
		high, _ := strconv.ParseFloat(item[2], 64)
		low, _ := strconv.ParseFloat(item[3], 64)
		cl, _ := strconv.ParseFloat(item[4], 64)
		vol, _ := strconv.ParseFloat(item[5], 64)

		candles = append(candles, Candle{
			Timestamp: time.Unix(0, tsMs*int64(time.Millisecond)),
			Open:      open,
			High:      high,
			Low:       low,
			Close:     cl,
			Volume:    vol,
		})
	}
	return candles, nil
}

// GetPrivateRequest calls an authenticated GET endpoint on Bybit V5.
// Used for reading live account state: wallet balance, positions, P&L.
// NOTE: For GET requests, Bybit V5 requires the raw query string to be
// included in the HMAC signature (unlike POST where it's the JSON body).
func (c *BybitClient) GetPrivateRequest(endpoint string) ([]byte, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"

	// Extract the query string (everything after '?') for signature computation.
	qs := ""
	if idx := strings.Index(endpoint, "?"); idx >= 0 {
		qs = endpoint[idx+1:]
	}
	signature := c.generateSignature(timestamp, recvWindow, qs)

	req, err := http.NewRequest("GET", c.BaseURL+endpoint, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-BAPI-API-KEY", c.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN", signature)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

// FetchTopSymbols retrieves the top N USDT perpetual symbols by 24h turnover.
// Uses the public tickers endpoint (no auth required).
func (c *BybitClient) FetchTopSymbols(n int) ([]string, error) {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/v5/market/tickers?category=linear")
	if err != nil {
		return nil, fmt.Errorf("tickers request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol      string `json:"symbol"`
				Turnover24h string `json:"turnover24h"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode tickers: %w", err)
	}
	if result.RetCode != 0 {
		return nil, fmt.Errorf("bybit tickers error [%d]: %s", result.RetCode, result.RetMsg)
	}

	type ticker struct {
		symbol   string
		turnover float64
	}
	var tickers []ticker
	for _, t := range result.Result.List {
		if !strings.HasSuffix(t.Symbol, "USDT") {
			continue
		}
		tv, _ := strconv.ParseFloat(t.Turnover24h, 64)
		tickers = append(tickers, ticker{symbol: t.Symbol, turnover: tv})
	}
	sort.Slice(tickers, func(i, j int) bool {
		return tickers[i].turnover > tickers[j].turnover
	})
	if n > len(tickers) {
		n = len(tickers)
	}
	symbols := make([]string, n)
	for i := 0; i < n; i++ {
		symbols[i] = tickers[i].symbol
	}
	return symbols, nil
}

// PostPrivateRequest routes authenticated execution calls directly to Bybit's engine
func (c *BybitClient) PostPrivateRequest(endpoint string, payload map[string]interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	timestamp := strconv.FormatInt(time.Now().UnixNano()/int64(time.Millisecond), 10)
	recvWindow := "5000"
	signature := c.generateSignature(timestamp, recvWindow, string(jsonData))

	req, err := http.NewRequest("POST", c.BaseURL+endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", c.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN", signature)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}
