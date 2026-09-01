package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ── CoinGecko API ─────────────────────────────────────────────────────────

// CoinGeckoNewsItem is one news article from CoinGecko's `/news` endpoint.
type CoinGeckoNewsItem struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Source      string `json:"source"`
	PublishedAt string `json:"published_at"`
}

// fetchCryptoNews fetches the latest crypto news headlines via CoinGecko.
// Uses the Pro API key if set, falls back to the public endpoint.
func fetchCryptoNews(coingeckoKey string, limit int) string {
	apiURL := "https://api.coingecko.com/api/v3/news"
	if limit <= 0 {
		limit = 10
	}

	reqURL := fmt.Sprintf("%s?limit=%d", apiURL, limit)
	if coingeckoKey != "" {
		reqURL = fmt.Sprintf("%s?x_cg_pro_api_key=%s&limit=%d", apiURL, coingeckoKey, limit)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(reqURL)
	if err != nil {
		return fmt.Sprintf("[CoinGecko news unavailable: %v]", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("[CoinGecko read error: %v]", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("[CoinGecko returned %d]", resp.StatusCode)
	}

	var result struct {
		Data []CoinGeckoNewsItem `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		// Try direct array format
		var items []CoinGeckoNewsItem
		if err2 := json.Unmarshal(body, &items); err2 != nil {
			return fmt.Sprintf("[CoinGecko parse error: %v]", err)
		}
		result.Data = items
	}

	if len(result.Data) == 0 {
		return "[No recent crypto news from CoinGecko]"
	}

	var b strings.Builder
	b.WriteString("📰 Top Crypto Headlines\n")
	for i, item := range result.Data {
		if i >= limit {
			break
		}
		when := item.PublishedAt
		if len(when) > 10 {
			when = when[:10]
		}
		b.WriteString(fmt.Sprintf("  %d. [%s] %s (%s)\n", i+1, when, item.Title, item.Source))
	}
	return b.String()
}

// ── StockTwits API (public, no key) ───────────────────────────────────────

// StockTwitsMsg is a single message from the StockTwits stream.
type StockTwitsMsg struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	Sentiment *struct {
		Basic string `json:"basic"` // "Bullish" or "Bearish"
	} `json:"sentiment"`
	User struct {
		Username       string `json:"username"`
		Followers      int    `json:"followers"`
		JoinDate       string `json:"join_date"`
	} `json:"user"`
}

// fetchStockTwitsSentiment fetches recent StockTwits messages for a ticker.
// Uses the public API — no key required.
// Maps crypto tickers: BTCUSDT → BTC.X, ETHUSDT → ETH.X
func fetchStockTwitsSentiment(symbol string, limit int) string {
	stSymbol := bybitToStockTwits(symbol)
	if stSymbol == "" {
		return ""
	}

	if limit <= 0 {
		limit = 10
	}

	apiURL := fmt.Sprintf("https://api.stocktwits.com/api/2/streams/symbol/%s.json", stSymbol)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var result struct {
		Messages []struct {
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
			Sentiment *struct {
				Basic string `json:"basic"`
			} `json:"sentiment"`
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"messages"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}

	if len(result.Messages) == 0 {
		return fmt.Sprintf("[No StockTwits messages for %s]", symbol)
	}

	var bullish, bearish, neutral int
	var recentBodies []string

	for i, msg := range result.Messages {
		if i >= limit {
			break
		}
		if i < 5 {
			recentBodies = append(recentBodies, fmt.Sprintf("  @%s: %s", msg.User.Username, truncate(msg.Body, 120)))
		}
		if msg.Sentiment != nil {
			switch msg.Sentiment.Basic {
			case "Bullish":
				bullish++
			case "Bearish":
				bearish++
			default:
				neutral++
			}
		} else {
			neutral++
		}
	}

	total := bullish + bearish + neutral
	bullPct := float64(bullish) / float64(total) * 100

	var b strings.Builder
	b.WriteString(fmt.Sprintf("💬 StockTwits %s: %d msgs, %.0f%% bullish (%d bull / %d bear / %d neutral)\n",
		symbol, total, bullPct, bullish, bearish, neutral))
	if bullPct > 60 {
		b.WriteString("  → Retail sentiment: OVERWHELMINGLY BULLISH (contrarian sell signal?)\n")
	} else if bullPct < 40 {
		b.WriteString("  → Retail sentiment: BEARISH (contrarian buy signal?)\n")
	} else {
		b.WriteString("  → Retail sentiment: MIXED\n")
	}
	b.WriteString("  Recent posts:\n")
	for _, rb := range recentBodies {
		b.WriteString(rb + "\n")
	}
	return b.String()
}

// bybitToStockTwits converts Bybit symbols (BTCUSDT) to StockTwits format (BTC.X).
func bybitToStockTwits(symbol string) string {
	base := strings.TrimSuffix(symbol, "USDT")
	if base == "" || base == symbol {
		return ""
	}
	// Common mappings
	switch base {
	case "BTC":
		return "BTC.X"
	case "ETH":
		return "ETH.X"
	case "SOL":
		return "SOL.X"
	case "XRP":
		return "XRP.X"
	case "ADA":
		return "ADA.X"
	case "DOT":
		return "DOT.X"
	case "AVAX":
		return "AVAX.X"
	case "LINK":
		return "LINK.X"
	case "BNB":
		return "BNB.X"
	case "DOGE":
		return "DOGE.X"
	case "MATIC":
		return "MATIC.X"
	case "UNI":
		return "UNI.X"
	case "ATOM":
		return "ATOM.X"
	case "NEAR":
		return "NEAR.X"
	case "APT":
		return "APT.X"
	case "SUI":
		return "SUI.X"
	case "ARB":
		return "ARB.X"
	case "OP":
		return "OP.X"
	default:
		return base + ".X"
	}
}

// ── Reddit RSS (public, no key) ───────────────────────────────────────────

// fetchRedditCrypto fetches the latest crypto-related posts from Reddit.
// Uses public RSS — no API key needed.
func fetchRedditCrypto(limit int) string {
	if limit <= 0 {
		limit = 8
	}

	// Try r/CryptoCurrency hot posts via RSS
	rssURL := "https://www.reddit.com/r/CryptoCurrency/hot/.rss"
	client := &http.Client{Timeout: 10 * time.Second}
	
	req, _ := http.NewRequest("GET", rssURL, nil)
	req.Header.Set("User-Agent", "hermes-trading-bot/1.0")
	
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("[Reddit RSS unavailable: %v]", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("[Reddit returned %d]", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("[Reddit read error: %v]", err)
	}

	// Simple RSS parsing — extract titles and links
	var titles []string
	var links []string
	text := string(body)
	
	// Find all <entry> blocks
	for len(titles) < limit {
		titleStart := strings.Index(text, "<title>")
		if titleStart < 0 {
			break
		}
		titleEnd := strings.Index(text[titleStart:], "</title>")
		if titleEnd < 0 {
			break
		}
		title := text[titleStart+7 : titleStart+titleEnd]
		
		linkStart := strings.Index(text[titleStart+titleEnd:], "<link href=\"")
		var link string
		if linkStart >= 0 {
			linkEnd := strings.Index(text[titleStart+titleEnd+linkStart+13:], "\"")
			if linkEnd >= 0 {
				link = text[titleStart+titleEnd+linkStart+13 : titleStart+titleEnd+linkStart+13+linkEnd]
			}
		}
		
		if !strings.Contains(title, "Comment") && !strings.HasPrefix(title, "[") {
			titles = append(titles, title)
			links = append(links, link)
		}
		
		text = text[titleStart+titleEnd+8:]
	}

	if len(titles) == 0 {
		return "[No Reddit crypto posts found]"
	}

	var b strings.Builder
	b.WriteString("🔴 Reddit r/CryptoCurrency Hot Posts\n")
	for i, title := range titles {
		if i >= limit {
			break
		}
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, truncate(title, 100)))
	}
	return b.String()
}

// ── Aggregated News Fetch ─────────────────────────────────────────────────

// MarketSentimentReport aggregates all news/social data for the AI Council.
type MarketSentimentReport struct {
	TopCryptoNews    string `json:"top_crypto_news"`
	SymbolSentiment  map[string]string `json:"symbol_sentiment"`
	RedditDiscussion string `json:"reddit_discussion"`
	FetchedAt        time.Time `json:"fetched_at"`
}

// FetchMarketSentiment pre-fetches all news and social data for the watchlist.
// Designed to be called once per cycle, before AI Council evaluation.
func FetchMarketSentiment(watchlist []string, coingeckoKey string) MarketSentimentReport {
	report := MarketSentimentReport{
		FetchedAt:       time.Now().UTC(),
		SymbolSentiment: make(map[string]string),
	}

	// Fetch global crypto news
	report.TopCryptoNews = fetchCryptoNews(coingeckoKey, 8)

	// Fetch Reddit
	report.RedditDiscussion = fetchRedditCrypto(6)

	// Fetch per-symbol StockTwits sentiment
	for _, sym := range watchlist {
		st := fetchStockTwitsSentiment(sym, 8)
		if st != "" {
			report.SymbolSentiment[sym] = st
		}
	}

	return report
}

// FormatForPrompt renders the sentiment report as a structured prompt block.
func (r MarketSentimentReport) FormatForPrompt() string {
	var b strings.Builder
	b.WriteString("─── MARKET SENTIMENT REPORT ───\n")

	if r.TopCryptoNews != "" && !strings.HasPrefix(r.TopCryptoNews, "[") {
		b.WriteString(r.TopCryptoNews + "\n")
	}

	if r.RedditDiscussion != "" && !strings.HasPrefix(r.RedditDiscussion, "[") {
		b.WriteString(r.RedditDiscussion + "\n")
	}

if len(r.SymbolSentiment) > 0 {
		b.WriteString("Per-Symbol Social Sentiment:\n")
		for _, st := range r.SymbolSentiment {
			if st != "" {
				b.WriteString(st + "\n")
			}
		}
	}

	return b.String()
}

// ── Utils ─────────────────────────────────────────────────────────────────

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}