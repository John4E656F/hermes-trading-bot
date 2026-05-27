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
	Args []string `json:"args,omitempty"` // Added omitempty for pings
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
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	ExecType      string `json:"execType"`
	ExecPrice     string `json:"execPrice"`
	ExecPnl       string `json:"execPnl"`
	ClosedSize    string `json:"closedSize"`
	StopOrderType string `json:"stopOrderType"`
	OrderID       string `json:"orderId"`
	ExecID        string `json:"execId"`
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

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("⚠️ Telegram API error (HTTP %d): %s", resp.StatusCode, string(body))
	}
}

func formatExecutionAlert(exec ExecutionData) string {
	emoji := "🔔"
	triggerType := exec.StopOrderType
	if triggerType == "" {
		triggerType = exec.ExecType
	}

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
// Bybit V5 WebSocket Auth
// ──────────────────────────────────────────────

func generateWSAuthPayload(apiKey, apiSecret string) ([]byte, error) {
	expires := strconv.FormatInt(time.Now().UnixMilli()+10000, 10)
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
	mu       sync.Mutex // Only protects WriteMessage
	stopChan chan struct{}
}

func NewExecutionListener(cfg *ListenerConfig) *ExecutionListener {
	return &ExecutionListener{
		cfg:      cfg,
		stopChan: make(chan struct{}),
	}
}

func (l *ExecutionListener) writeJSON(v interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return fmt.Errorf("connection is nil")
	}
	return l.conn.WriteJSON(v)
}

func (l *ExecutionListener) writeRaw(data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return fmt.Errorf("connection is nil")
	}
	return l.conn.WriteMessage(websocket.TextMessage, data)
}

func (l *ExecutionListener) connect() error {
	l.mu.Lock()
	if l.conn != nil {
		l.conn.Close()
	}
	l.mu.Unlock()

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(l.cfg.BybitWS, nil)
	if err != nil {
		return fmt.Errorf("dial error: %w", err)
	}

	l.mu.Lock()
	l.conn = conn
	l.mu.Unlock()

	log.Printf("🔌 Connected to %s", l.cfg.BybitWS)
	return nil
}

func (l *ExecutionListener) authenticate() error {
	payload, err := generateWSAuthPayload(l.cfg.BybitAPIKey, l.cfg.BybitAPISecret)
	if err != nil {
		return fmt.Errorf("auth payload error: %w", err)
	}

	if err := l.writeRaw(payload); err != nil {
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
	if err := l.writeJSON(req); err != nil {
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

// Heartbeat Loop required by Bybit to keep connection alive
func (l *ExecutionListener) pingLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopChan:
			return
		case <-ticker.C:
			// Send {"op":"ping"} periodically
			req := WSRequest{Op: "ping"}
			if err := l.writeJSON(req); err != nil {
				log.Printf("⚠️ Ping error: %v", err)
				return // Let the read loop handle the disconnect
			}
		}
	}
}

func (l *ExecutionListener) readLoop() {
	for {
		select {
		case <-l.stopChan:
			return
		default:
		}

		// NO MUTEX LOCK HERE. ReadMessage blocks.
		_, msg, err := l.conn.ReadMessage()
		if err != nil {
			log.Printf("⚠️ Read error: %v — triggering reconnect", err)
			return
		}

		l.processMessage(msg)
	}
}

func (l *ExecutionListener) processMessage(raw []byte) {
	// Fast check for pong response (heartbeat ack)
	if strings.Contains(string(raw), `"op":"pong"`) || strings.Contains(string(raw), `"ret_msg":"pong"`) {
		return
	}

	var envelope struct {
		Topic string          `json:"topic"`
		Type  string          `json:"type"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}

	if envelope.Topic != "execution" || envelope.Type == "snapshot" {
		return
	}

	var executions []ExecutionData
	if err := json.Unmarshal(envelope.Data, &executions); err != nil {
		log.Printf("⚠️ Failed to parse execution data: %v", err)
		return
	}

	for _, exec := range executions {
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

		alert := formatExecutionAlert(exec)
		sendTelegramAlert(l.cfg, alert)
	}
}

// ──────────────────────────────────────────────
// Simplified Lifecycle Manager
// ──────────────────────────────────────────────

func (l *ExecutionListener) Run() {
	baseDelay := 1 * time.Second
	maxDelay := 60 * time.Second
	delay := baseDelay

	for {
		select {
		case <-l.stopChan:
			return
		default:
		}

		if err := l.connect(); err != nil {
			log.Printf("❌ Connection error: %v", err)
			time.Sleep(delay)
			delay = minDuration(delay*2, maxDelay)
			continue
		}

		if err := l.authenticate(); err != nil {
			log.Printf("❌ Auth error: %v", err)
			l.conn.Close()
			time.Sleep(delay)
			delay = minDuration(delay*2, maxDelay)
			continue
		}

		if err := l.subscribe("execution.linear"); err != nil {
			log.Printf("❌ Subscribe error: %v", err)
			l.conn.Close()
			time.Sleep(delay)
			delay = minDuration(delay*2, maxDelay)
			continue
		}

		// Reset delay upon successful connection
		delay = baseDelay
		log.Println("✅ Listening for live executions...")

		// Start ping loop for THIS connection; cancel it when readLoop exits
		pingDone := make(chan struct{})
		go func() {
			defer close(pingDone)
			l.pingLoop()
		}()

		// Blocks until the connection is dropped or an error occurs
		l.readLoop()

		// readLoop exited — connection is dead; stop the ping loop
		// before reconnecting so a fresh one is created next cycle
		l.mu.Lock()
		if l.conn != nil {
			l.conn.Close()
			l.conn = nil
		}
		l.mu.Unlock()
		<-pingDone
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
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
	log.Println("🎧 Hermes Execution Listener v1.1")

	cfg := loadListenerConfig()

	if cfg.BybitAPIKey == "" || cfg.BybitAPISecret == "" {
		log.Fatalf("❌ BYBIT_API_KEY and BYBIT_API_SECRET must be set")
	}

	log.Println("🔧 Listener configuration loaded")
	if cfg.TelegramToken != "" && cfg.TelegramChatID != "" {
		log.Println("📱 Telegram alerts: ENABLED")
	} else {
		log.Println("📱 Telegram alerts: DISABLED")
	}

	listener := NewExecutionListener(cfg)

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
