package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/nats-io/nats.go"
)

// Account — one WhatsApp Business account instance.
type Account struct {
	Name          string // lowercase name from ENV (e.g. "my_account")
	PhoneNumberID string // WhatsApp Cloud API phone number ID
	AccessToken   string // Meta access token
}

// --- WhatsApp Webhook Payload Structs ---

type WebhookPayload struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

type Entry struct {
	ID      string   `json:"id"`
	Changes []Change `json:"changes"`
}

type Change struct {
	Value ChangeValue `json:"value"`
	Field string      `json:"field"`
}

type ChangeValue struct {
	MessagingProduct string    `json:"messaging_product"`
	Metadata         Metadata  `json:"metadata"`
	Contacts         []Contact `json:"contacts,omitempty"`
	Messages         []Message `json:"messages,omitempty"`
	Statuses         []Status  `json:"statuses,omitempty"`
	Errors           []WAError `json:"errors,omitempty"`
}

type Metadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

type Contact struct {
	Profile ProfileInfo `json:"profile"`
	WaID    string      `json:"wa_id"`
}

type ProfileInfo struct {
	Name string `json:"name"`
}

type Message struct {
	From        string           `json:"from"`
	ID          string           `json:"id"`
	Timestamp   string           `json:"timestamp"`
	Type        string           `json:"type"`
	Text        *TextContent     `json:"text,omitempty"`
	Image       *MediaContent    `json:"image,omitempty"`
	Video       *MediaContent    `json:"video,omitempty"`
	Audio       *MediaContent    `json:"audio,omitempty"`
	Document    *DocumentContent `json:"document,omitempty"`
	Sticker     *MediaContent    `json:"sticker,omitempty"`
	Location    *LocationContent `json:"location,omitempty"`
	Contacts    []ContactCard    `json:"contacts,omitempty"`
	Button      *ButtonContent   `json:"button,omitempty"`
	Context     *ContextInfo     `json:"context,omitempty"`
	Reaction    *ReactionContent `json:"reaction,omitempty"`
	Interactive *json.RawMessage `json:"interactive,omitempty"`
	Errors      []WAError        `json:"errors,omitempty"`
}

type TextContent struct {
	Body string `json:"body"`
}

type MediaContent struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Caption  string `json:"caption,omitempty"`
}

type DocumentContent struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Filename string `json:"filename,omitempty"`
}

type LocationContent struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
}

type ContactCard struct {
	Name   json.RawMessage `json:"name,omitempty"`
	Phones json.RawMessage `json:"phones,omitempty"`
}

type ButtonContent struct {
	Text    string `json:"text"`
	Payload string `json:"payload"`
}

type ContextInfo struct {
	From string `json:"from"`
	ID   string `json:"id"`
}

type ReactionContent struct {
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
}

type Status struct {
	ID           string           `json:"id"`
	Status       string           `json:"status"`
	Timestamp    string           `json:"timestamp"`
	RecipientID  string           `json:"recipient_id"`
	Conversation *json.RawMessage `json:"conversation,omitempty"`
	Pricing      *json.RawMessage `json:"pricing,omitempty"`
	Errors       []WAError        `json:"errors,omitempty"`
}

type WAError struct {
	Code    int    `json:"code"`
	Title   string `json:"title"`
	Message string `json:"message,omitempty"`
	Details string `json:"error_data,omitempty"`
}

// --- Outgoing request structs ---

type RawRequest struct {
	Method string          `json:"method,omitempty"` // HTTP method override (default POST)
	Path   string          `json:"path,omitempty"`   // custom path override (default /{phone_id}/messages)
	Body   json.RawMessage `json:"body"`
}

func main() {
	_ = godotenv.Load()

	natsURL := env("NATS_URL", "nats://localhost:4222")
	port := env("PORT", "8080")
	verifyToken := os.Getenv("WEBHOOK_VERIFY_TOKEN")
	appSecret := os.Getenv("APP_SECRET")
	apiVersion := env("API_VERSION", "v25.0")

	// Discover accounts from WA_* env vars
	accounts := discoverAccounts()
	if len(accounts) == 0 {
		log.Fatal("No accounts configured. Set WA_<NAME>=<phone_number_id>:<access_token> environment variables.")
	}

	log.Printf("Starting whatsapp-bot-nats | NATS: %s | Accounts: %d", natsURL, len(accounts))

	// NATS connect
	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("NATS connect: %v", err)
	}
	defer nc.Close()

	// Build account lookup by phone_number_id
	accountByPhone := make(map[string]Account, len(accounts))

	for _, acct := range accounts {
		a := acct
		log.Printf("[%s] phone_number_id=%s", a.Name, a.PhoneNumberID)

		// Subscribe to outgoing subjects: whatsapp.<name>.out.>
		subject := fmt.Sprintf("whatsapp.%s.out.>", a.Name)
		_, err := nc.Subscribe(subject, func(msg *nats.Msg) {
			handleOutgoing(nc, a, msg, apiVersion)
		})
		if err != nil {
			log.Fatalf("[%s] subscribe %s: %v", a.Name, subject, err)
		}
		log.Printf("[%s] subscribed to %s", a.Name, subject)

		accountByPhone[a.PhoneNumberID] = a
	}

	// HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", makeWebhookHandler(nc, accountByPhone, verifyToken, appSecret))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start HTTP server
	go func() {
		log.Printf("HTTP server listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server: %v", err)
		}
	}()

	// Graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down...")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)

	nc.Drain()
	log.Println("Done.")
}

// discoverAccounts scans environment for WA_* variables.
// Format: WA_<NAME>=<phone_number_id>:<access_token>
func discoverAccounts() []Account {
	var accounts []Account
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		if !strings.HasPrefix(key, "WA_") || val == "" {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(key, "WA_"))
		valueParts := strings.SplitN(val, ":", 2)
		if len(valueParts) != 2 {
			log.Printf("WARNING: invalid WA_%s format, expected <phone_number_id>:<access_token>", strings.ToUpper(name))
			continue
		}
		accounts = append(accounts, Account{
			Name:          name,
			PhoneNumberID: valueParts[0],
			AccessToken:   valueParts[1],
		})
	}
	return accounts
}

// makeWebhookHandler returns an HTTP handler for WhatsApp webhooks.
// Handles both GET (verification) and POST (incoming events).
func makeWebhookHandler(nc *nats.Conn, accountByPhone map[string]Account, verifyToken, appSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleVerification(w, r, verifyToken)
		case http.MethodPost:
			handleIncoming(w, r, nc, accountByPhone, appSecret)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handleVerification handles the WhatsApp webhook verification (GET /webhook).
func handleVerification(w http.ResponseWriter, r *http.Request, verifyToken string) {
	mode := r.URL.Query().Get("hub.mode")
	token := r.URL.Query().Get("hub.verify_token")
	challenge := r.URL.Query().Get("hub.challenge")

	log.Printf("Webhook verification request: URL=%s mode=%q token=%q challenge=%q configured_token_len=%d",
		r.URL.String(), mode, token, challenge, len(verifyToken))

	if mode == "subscribe" && token == verifyToken {
		log.Printf("Webhook verified successfully")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(challenge))
		return
	}

	log.Printf("Webhook verification failed: mode=%q token_match=%v (got_len=%d, want_len=%d)",
		mode, token == verifyToken, len(token), len(verifyToken))
	http.Error(w, "forbidden", http.StatusForbidden)
}

// handleIncoming handles incoming WhatsApp webhook events (POST /webhook).
func handleIncoming(w http.ResponseWriter, r *http.Request, nc *nats.Conn, accountByPhone map[string]Account, appSecret string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// Validate HMAC signature if APP_SECRET is configured
	if appSecret != "" {
		signature := r.Header.Get("X-Hub-Signature-256")
		if !validateSignature(body, signature, appSecret) {
			log.Printf("Invalid webhook signature")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	// Always respond 200 quickly to Meta
	w.WriteHeader(http.StatusOK)

	// Process entries
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			switch change.Field {
			case "messages":
				phoneID := change.Value.Metadata.PhoneNumberID
				acct, ok := accountByPhone[phoneID]
				if !ok {
					log.Printf("Unknown phone_number_id: %s", phoneID)
					continue
				}
				publishEvents(nc, acct, change.Value, body)

			case "account_update", "business_status_update", "phone_number_quality_update":
				// Account-level events — publish raw change value to whatsapp.event.<field>
				changeData, err := json.Marshal(change.Value)
				if err != nil {
					log.Printf("marshal %s: %v", change.Field, err)
					continue
				}
				subject := "whatsapp.event." + change.Field
				if err := nc.Publish(subject, changeData); err != nil {
					log.Printf("publish %s: %v", subject, err)
				}
				log.Printf("← %s (entry=%s)", change.Field, entry.ID)

			default:
				log.Printf("Unhandled webhook field: %s", change.Field)
			}
		}
	}
}

// validateSignature validates X-Hub-Signature-256 HMAC signature.
func validateSignature(body []byte, signature, appSecret string) bool {
	if signature == "" {
		return false
	}

	// Signature format: "sha256=<hex>"
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(sig, expected)
}

// publishEvents publishes webhook events to NATS subjects.
func publishEvents(nc *nats.Conn, acct Account, value ChangeValue, rawPayload []byte) {
	prefix := fmt.Sprintf("whatsapp.%s.in", acct.Name)

	// Always publish full webhook payload
	if err := nc.Publish(prefix+".webhook", rawPayload); err != nil {
		log.Printf("[%s] publish webhook: %v", acct.Name, err)
	}

	// Publish individual messages
	for _, msg := range value.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[%s] marshal message: %v", acct.Name, err)
			continue
		}

		// Publish to in.message
		if err := nc.Publish(prefix+".message", data); err != nil {
			log.Printf("[%s] publish message: %v", acct.Name, err)
		}

		// Also publish to type-specific subject: in.message.<type>
		if msg.Type != "" {
			if err := nc.Publish(prefix+".message."+msg.Type, data); err != nil {
				log.Printf("[%s] publish message.%s: %v", acct.Name, msg.Type, err)
			}
		}

		log.Printf("[%s] ← message %s from %s (type=%s)", acct.Name, msg.ID, msg.From, msg.Type)
	}

	// Publish statuses
	for _, status := range value.Statuses {
		data, err := json.Marshal(status)
		if err != nil {
			log.Printf("[%s] marshal status: %v", acct.Name, err)
			continue
		}

		if err := nc.Publish(prefix+".status", data); err != nil {
			log.Printf("[%s] publish status: %v", acct.Name, err)
		}

		log.Printf("[%s] ← status %s → %s (%s)", acct.Name, status.ID, status.RecipientID, status.Status)
	}

	// Publish errors
	for _, waErr := range value.Errors {
		data, err := json.Marshal(waErr)
		if err != nil {
			continue
		}
		if err := nc.Publish(prefix+".error", data); err != nil {
			log.Printf("[%s] publish error: %v", acct.Name, err)
		}
		log.Printf("[%s] ← error %d: %s", acct.Name, waErr.Code, waErr.Title)
	}
}

// handleOutgoing handles NATS messages on whatsapp.<name>.out.* subjects.
func handleOutgoing(nc *nats.Conn, acct Account, msg *nats.Msg, apiVersion string) {
	// Extract action from subject: whatsapp.<name>.out.<action>
	parts := strings.Split(msg.Subject, ".")
	if len(parts) < 4 {
		log.Printf("[%s] bad out subject: %s", acct.Name, msg.Subject)
		respondError(msg, "invalid subject format")
		return
	}
	action := parts[3]

	var apiURL string
	var payload []byte

	baseURL := fmt.Sprintf("https://graph.facebook.com/%s/%s", apiVersion, acct.PhoneNumberID)

	switch action {
	case "raw":
		// Raw: allows custom path and body
		var raw RawRequest
		if err := json.Unmarshal(msg.Data, &raw); err != nil {
			log.Printf("[%s] bad raw request: %v", acct.Name, err)
			respondError(msg, fmt.Sprintf("bad raw request: %v", err))
			return
		}
		if raw.Path != "" {
			apiURL = fmt.Sprintf("https://graph.facebook.com/%s/%s", apiVersion, strings.TrimPrefix(raw.Path, "/"))
		} else {
			apiURL = baseURL + "/messages"
		}
		payload = raw.Body

	case "sendMessage":
		apiURL = baseURL + "/messages"
		payload = wrapTextMessage(msg.Data)

	case "sendImage":
		apiURL = baseURL + "/messages"
		payload = wrapMediaMessage(msg.Data, "image")

	case "sendDocument":
		apiURL = baseURL + "/messages"
		payload = wrapMediaMessage(msg.Data, "document")

	case "sendVideo":
		apiURL = baseURL + "/messages"
		payload = wrapMediaMessage(msg.Data, "video")

	case "sendAudio":
		apiURL = baseURL + "/messages"
		payload = wrapMediaMessage(msg.Data, "audio")

	case "sendLocation":
		apiURL = baseURL + "/messages"
		payload = wrapLocationMessage(msg.Data)

	case "sendContact":
		apiURL = baseURL + "/messages"
		payload = wrapContactMessage(msg.Data)

	case "sendTemplate":
		apiURL = baseURL + "/messages"
		payload = wrapTemplateMessage(msg.Data)

	case "sendReaction":
		apiURL = baseURL + "/messages"
		payload = wrapReactionMessage(msg.Data)

	case "markRead":
		apiURL = baseURL + "/messages"
		payload = wrapMarkRead(msg.Data)

	case "sendSticker":
		apiURL = baseURL + "/messages"
		payload = wrapMediaMessage(msg.Data, "sticker")

	default:
		// Any unknown action → direct post to /messages with the data as-is
		apiURL = baseURL + "/messages"
		payload = msg.Data
	}

	// Call WhatsApp Cloud API
	result, err := callWhatsAppAPI(apiURL, acct.AccessToken, payload)
	if err != nil {
		log.Printf("[%s] API %s error: %v", acct.Name, action, err)
		respondError(msg, fmt.Sprintf("API error: %v", err))
		return
	}

	// Check for API errors in response
	var apiResp struct {
		Error *struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      int    `json:"code"`
			FbTraceID string `json:"fbtrace_id"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(result, &apiResp); err == nil && apiResp.Error != nil {
		log.Printf("[%s] → %s FAIL [%d] %s", acct.Name, action, apiResp.Error.Code, apiResp.Error.Message)

		// Publish error to NATS stream
		errPayload, _ := json.Marshal(map[string]interface{}{
			"action":  action,
			"code":    apiResp.Error.Code,
			"message": apiResp.Error.Message,
			"type":    apiResp.Error.Type,
			"request": json.RawMessage(payload),
		})
		errSubject := fmt.Sprintf("whatsapp.%s.error", acct.Name)
		if pubErr := nc.Publish(errSubject, errPayload); pubErr != nil {
			log.Printf("[%s] publish error event: %v", acct.Name, pubErr)
		}
	} else {
		log.Printf("[%s] → %s OK", acct.Name, action)
	}

	// Reply if request/reply pattern
	if msg.Reply != "" {
		msg.Respond(result)
	}
}

// --- Message wrapping helpers ---
// These functions take user-friendly JSON and wrap it into WhatsApp Cloud API format.

func wrapTextMessage(data []byte) []byte {
	var req struct {
		To   string `json:"to"`
		Text string `json:"text"`
		// Optional fields
		PreviewURL *bool        `json:"preview_url,omitempty"`
		Context    *ContextInfo `json:"context,omitempty"`
		ReplyTo    string       `json:"reply_to,omitempty"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return data // fallback to raw
	}

	msg := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                req.To,
		"type":              "text",
		"text":              map[string]interface{}{"body": req.Text, "preview_url": req.PreviewURL != nil && *req.PreviewURL},
	}
	if req.ReplyTo != "" {
		msg["context"] = map[string]string{"message_id": req.ReplyTo}
	} else if req.Context != nil {
		msg["context"] = req.Context
	}

	result, _ := json.Marshal(msg)
	return result
}

func wrapMediaMessage(data []byte, mediaType string) []byte {
	var req struct {
		To       string `json:"to"`
		MediaID  string `json:"media_id,omitempty"`
		Link     string `json:"link,omitempty"`
		Caption  string `json:"caption,omitempty"`
		Filename string `json:"filename,omitempty"`
		ReplyTo  string `json:"reply_to,omitempty"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return data
	}

	mediaObj := map[string]interface{}{}
	if req.MediaID != "" {
		mediaObj["id"] = req.MediaID
	} else if req.Link != "" {
		mediaObj["link"] = req.Link
	}
	if req.Caption != "" {
		mediaObj["caption"] = req.Caption
	}
	if req.Filename != "" {
		mediaObj["filename"] = req.Filename
	}

	msg := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                req.To,
		"type":              mediaType,
		mediaType:           mediaObj,
	}
	if req.ReplyTo != "" {
		msg["context"] = map[string]string{"message_id": req.ReplyTo}
	}

	result, _ := json.Marshal(msg)
	return result
}

func wrapLocationMessage(data []byte) []byte {
	var req struct {
		To        string  `json:"to"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Name      string  `json:"name,omitempty"`
		Address   string  `json:"address,omitempty"`
		ReplyTo   string  `json:"reply_to,omitempty"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return data
	}

	loc := map[string]interface{}{
		"latitude":  req.Latitude,
		"longitude": req.Longitude,
	}
	if req.Name != "" {
		loc["name"] = req.Name
	}
	if req.Address != "" {
		loc["address"] = req.Address
	}

	msg := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                req.To,
		"type":              "location",
		"location":          loc,
	}
	if req.ReplyTo != "" {
		msg["context"] = map[string]string{"message_id": req.ReplyTo}
	}

	result, _ := json.Marshal(msg)
	return result
}

func wrapContactMessage(data []byte) []byte {
	var req struct {
		To       string          `json:"to"`
		Contacts json.RawMessage `json:"contacts"`
		ReplyTo  string          `json:"reply_to,omitempty"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return data
	}

	msg := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                req.To,
		"type":              "contacts",
		"contacts":          req.Contacts,
	}
	if req.ReplyTo != "" {
		msg["context"] = map[string]string{"message_id": req.ReplyTo}
	}

	result, _ := json.Marshal(msg)
	return result
}

func wrapTemplateMessage(data []byte) []byte {
	var req struct {
		To       string          `json:"to"`
		Template json.RawMessage `json:"template"`
		ReplyTo  string          `json:"reply_to,omitempty"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return data
	}

	msg := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                req.To,
		"type":              "template",
		"template":          req.Template,
	}
	if req.ReplyTo != "" {
		msg["context"] = map[string]string{"message_id": req.ReplyTo}
	}

	result, _ := json.Marshal(msg)
	return result
}

func wrapReactionMessage(data []byte) []byte {
	var req struct {
		To        string `json:"to"`
		MessageID string `json:"message_id"`
		Emoji     string `json:"emoji"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return data
	}

	msg := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                req.To,
		"type":              "reaction",
		"reaction": map[string]string{
			"message_id": req.MessageID,
			"emoji":      req.Emoji,
		},
	}

	result, _ := json.Marshal(msg)
	return result
}

func wrapMarkRead(data []byte) []byte {
	var req struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return data
	}

	msg := map[string]interface{}{
		"messaging_product": "whatsapp",
		"status":            "read",
		"message_id":        req.MessageID,
	}

	result, _ := json.Marshal(msg)
	return result
}

// callWhatsAppAPI sends a POST request to the WhatsApp Cloud API.
func callWhatsAppAPI(url, accessToken string, payload []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return body, nil
}

// respondError sends an error response if reply subject is set.
func respondError(msg *nats.Msg, errMsg string) {
	if msg.Reply == "" {
		return
	}
	resp := map[string]string{"error": errMsg}
	data, _ := json.Marshal(resp)
	msg.Respond(data)
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
