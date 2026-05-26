package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// ──────────────────────────────────────────────
// Configuration
// ──────────────────────────────────────────────

type ListenerConfig struct {
	BybitWS        string
	BybitAPIKey    string
	BybitAPISecret string
	TelegramToken  string
	TelegramChatID string
}

func loadListenerConfig() *ListenerConfig {
	return &ListenerConfig{
		BybitWS:        getEnv("BYBIT_WS_URL", "wss://stream.bybit.com/v5/private"),
		BybitAPIKey:    os.Getenv("BYBIT_API_KEY"),
		BybitAPISecret: os.Getenv("BYBIT_API_SECRET"),
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID: os.Getenv("TELEGRAM_CHAT_ID"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ──────────────────────────────────────────────
// Bybit WebSocket Message Types
// ──────────────────────────────────────────────

type WSRequest struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

type WSAuthRequest struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

type WSResponse struct {
	Success bool            `json:"success"`
	RetMsg  string          `json:"ret_msg"`
	ConnID  string          `json:"conn_id"`
	Type    string          `json:"type"`
	Topic   string          `json:"topic"`
	Data    json.RawMessage `json:"data"`
}

type ExecutionData struct {
	Symbol       string `json:"symbol"`
	Side         string `json:"side"`
	ExecType     string `json:"execType"`
	ExecPrice    string `json:"execPrice"`
	ExecPnl      string `json:"execPnl"`
	ClosedSize   string `json:"closedSize"`
	StopOrderType string `json:"stopOrderType"`
	OrderID      string `json:"orderId"`
	ExecID       string `json:"execId"`
	ExecTime     string `json:"execTime"`
	LeavesQty    string `json:"leavesQty"`
	CumExecQty   string `json:"cumExecQty"`
	CumExecValue string `json:"cumExecValue"`
	OrderType    string `json:"orderType"`
}

// ──────────────────────────────────────────────
// Telegram Alert
// ──────────────────────────────────────────────

func sendTelegramAlert(cfg *ListenerConfig, message string) {
	if cfg.TelegramToken == "" || cfg.TelegramChatID == "" {
		log.Println("⚠️ TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set — skipping alert")
		return
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.TelegramToken)
	payload := url.Values{}
	payload.Set("chat_id", cfg.TelegramChatID)
	payload.Set("text", message)
	payload.Set("parse_mode", "HTML")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(apiURL, payload)
	if err != nil {
		log.Printf("⚠️ Telegram send error: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("⚠️ Telegram API error (HTTP %d): %s", resp.StatusCode, string(body))
	}
}

func formatExecutionAlert(exec ExecutionData) string {
	emoji := "🔔"
	triggerType := exec.StopOrderType
	if triggerType == "" {
		triggerType = exec.ExecType
	}

	// Determine emoji by trigger type
	upperTrigger := strings.ToUpper(triggerType)
	if strings.Contains(upperTrigger, "STOPLOSS") || strings.Contains(upperTrigger, "SL") {
		emoji = "🛑"
	} else if strings.Contains(upperTrigger, "TAKEPROFIT") || strings.Contains(upperTrigger, "TP") {
		emoji = "✅"
	} else if strings.Contains(upperTrigger, "TRAIL") {
		emoji = "🎯"
	}

	execPnl := exec.ExecPnl
	if execPnl == "" {
		execPnl = "0.00"
	}

	pnlFloat, _ := strconv.ParseFloat(execPnl, 64)
	pnlIcon := "▪️"
	if pnlFloat > 0 {
		pnlIcon = "🟢"
	} else if pnlFloat < 0 {
		pnlIcon = "🔴"
	}

	side := strings.ToUpper(exec.Side)
	action := fmt.Sprintf("%s (Position Closed)", side)

	return fmt.Sprintf(
		"%s <b>Real-Time Trade Alert!</b>\n"+
		"• Asset: %s\n"+
		"• Action: %s\n"+
		"• Trigger: %s\n"+
		"• Exec Price: $%s\n"+
		"• %s Realized P&L: $%s USDT\n"+
		"• Exec ID: %s",
		emoji, exec.Symbol, action, triggerType, exec.ExecPrice,
		pnlIcon, execPnl, exec.ExecID,
	)
}

// ──────────────────────────────────────────────
// Bybit V5 WebSocket Auth (derived from docs)
// ──────────────────────────────────────────────

func generateWSAuthPayload(apiKey, apiSecret string) ([]byte, error) {
	expires := strconv.FormatInt(time.Now().UnixMilli()+10000, 10) // 10s expiry
	signPayload := "GET/realtime" + expires

	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write([]byte(signPayload))
	signature := hex.EncodeToString(mac.Sum(nil))

	req := map[string]interface{}{
		"op":   "auth",
		"args": []string{apiKey, expires, signature},
	}
	return json.Marshal(req)
}

// ──────────────────────────────────────────────
// WebSocket Engine
// ──────────────────────────────────────────────

type ExecutionListener struct {
	cfg      *ListenerConfig
	conn     *websocket.Conn
	mu       sync.Mutex
	stopChan chan struct{}
}

func NewExecutionListener(cfg *ListenerConfig) *ExecutionListener {
	return &ExecutionListener{
		cfg:      cfg,
		stopChan: make(chan struct{}),
	}
}

func (l *ExecutionListener) connect() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.conn != nil {
		l.conn.Close()
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(l.cfg.BybitWS, nil)
	if err != nil {
		return fmt.Errorf("dial error: %w", err)
	}
	l.conn = conn
	log.Printf("🔌 Connected to %s", l.cfg.BybitWS)
	return nil
}

func (l *ExecutionListener) authenticate() error {
	payload, err := generateWSAuthPayload(l.cfg.BybitAPIKey, l.cfg.BybitAPISecret)
	if err != nil {
		return fmt.Errorf("auth payload error: %w", err)
	}

	l.mu.Lock()
	err = l.conn.WriteMessage(websocket.TextMessage, payload)
	l.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth write error: %w", err)
	}

	// Read auth response
	_, msg, err := l.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("auth read error: %w", err)
	}

	var resp WSResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fmt.Errorf("auth parse error: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("auth failed: %s", resp.RetMsg)
	}
	log.Println("🔐 WebSocket authenticated")
	return nil
}

func (l *ExecutionListener) subscribe(topic string) error {
	req := WSRequest{
		Op:   "subscribe",
		Args: []string{topic},
	}
	payload, _ := json.Marshal(req)

	l.mu.Lock()
	err := l.conn.WriteMessage(websocket.TextMessage, payload)
	l.mu.Unlock()
	if err != nil {
		return fmt.Errorf("subscribe write error: %w", err)
	}

	// Read subscribe response
	_, msg, err := l.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("subscribe read error: %w", err)
	}

	var resp WSResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fmt.Errorf("subscribe parse error: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("subscribe failed: %s", resp.RetMsg)
	}
	log.Printf("📡 Subscribed to %s", topic)
	return nil
}

func (l *ExecutionListener) readLoop() {
	for {
		select {
		case <-l.stopChan:
			return
		default:
		}

		l.mu.Lock()
		_, msg, err := l.conn.ReadMessage()
		l.mu.Unlock()

		if err != nil {
			log.Printf("⚠️ Read error: %v — triggering reconnect", err)
			return
		}

		l.processMessage(msg)
	}
}

func (l *ExecutionListener) processMessage(raw []byte) {
	var envelope struct {
		Topic string          `json:"topic"`
		Type  string          `json:"type"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}

	// Skip non-execution topics and heartbeats
	if envelope.Topic != "execution" || envelope.Type == "snapshot" {
		return
	}

	// Parse execution data array
	var executions []ExecutionData
	if err := json.Unmarshal(envelope.Data, &executions); err != nil {
		log.Printf("⚠️ Failed to parse execution data: %v", err)
		return
	}

	for _, exec := range executions {
		// Filter: position close or has P&L
		closedSize, _ := strconv.ParseFloat(exec.ClosedSize, 64)
		execPnl, _ := strconv.ParseFloat(exec.ExecPnl, 64)

		if closedSize <= 0 && execPnl == 0 {
			continue
		}

		log.Printf(
			"📊 Execution: %s | Side: %s | Price: %s | PnL: %s | Trigger: %s | Closed: %s",
			exec.Symbol, exec.Side, exec.ExecPrice, exec.ExecPnl,
			exec.StopOrderType, exec.ClosedSize,
		)

		// Dispatch Telegram alert
		alert := formatExecutionAlert(exec)
		sendTelegramAlert(l.cfg, alert)
	}
}

// ──────────────────────────────────────────────
// Reconnect Loop with Exponential Backoff
// ──────────────────────────────────────────────

func (l *ExecutionListener) Run() {
	baseDelay := 1 * time.Second
	maxDelay := 60 * time.Second

	for {
		// Connect
		if err := l.connect(); err != nil {
			log.Fatalf("❌ Fatal connection error: %v", err)
		}

		// Authenticate
		if err := l.authenticate(); err != nil {
			log.Printf("❌ Auth error: %v — retrying...", err)
			l.conn.Close()
			time.Sleep(baseDelay)
			continue
		}

		// Subscribe
		if err := l.subscribe("execution.linear"); err != nil {
			log.Printf("❌ Subscribe error: %v — retrying...", err)
			l.conn.Close()
			time.Sleep(baseDelay)
			continue
		}

		// Read loop — blocks until disconnect
		l.readLoop()

		// Reconnect with exponential backoff
		delay := baseDelay
		for attempts := 0; attempts < 10; attempts++ {
			log.Printf("🔄 Reconnecting in %v (attempt %d)...", delay, attempts+1)

			select {
			case <-l.stopChan:
				return
			case <-time.After(delay):
			}

			if err := l.connect(); err != nil {
				log.Printf("⚠️ Reconnect failed: %v", err)
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
				continue
			}

			// Re-auth after reconnect
			if err := l.authenticate(); err != nil {
				log.Printf("⚠️ Re-auth failed: %v", err)
				l.conn.Close()
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
				continue
			}

			// Re-subscribe
			if err := l.subscribe("execution.linear"); err != nil {
				log.Printf("⚠️ Re-subscribe failed: %v", err)
				l.conn.Close()
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
				continue
			}

			log.Println("✅ Reconnected successfully")
			break
		}
	}
}

func (l *ExecutionListener) Stop() {
	close(l.stopChan)
	l.mu.Lock()
	if l.conn != nil {
		l.conn.Close()
	}
	l.mu.Unlock()
}

// ──────────────────────────────────────────────
// Entry Point
// ──────────────────────────────────────────────

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("🎧 Hermes Execution Listener v1.0")

	cfg := loadListenerConfig()

	if cfg.BybitAPIKey == "" || cfg.BybitAPISecret == "" {
		log.Fatalf("❌ BYBIT_API_KEY and BYBIT_API_SECRET must be set")
	}

	log.Println("🔧 Listener configuration loaded")
	if cfg.TelegramToken != "" && cfg.TelegramChatID != "" {
		log.Println("📱 Telegram alerts: ENABLED")
	} else {
		log.Println("📱 Telegram alerts: DISABLED (set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID)")
	}

	listener := NewExecutionListener(cfg)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("🛑 Shutting down listener...")
		listener.Stop()
		os.Exit(0)
	}()

	listener.Run()
}