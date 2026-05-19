package services

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
	"google.golang.org/protobuf/proto"
)

type cacheEntry struct {
	data      []map[string]interface{}
	timestamp time.Time
}

type GoogleSheetService struct {
	srv           *sheets.Service
	spreadsheetID string
	cache         map[string]cacheEntry
	cacheTTL      time.Duration
	mu            sync.RWMutex
}

func NewGoogleSheetService(credentialsPath string, sheetURL string) *GoogleSheetService {
	ctx := context.Background()
	cacheTTLSeconds := 300

	re := regexp.MustCompile(`/d/([a-zA-Z0-9-_]+)`)
	matches := re.FindStringSubmatch(sheetURL)
	var spID string
	if len(matches) > 1 {
		spID = matches[1]
	}

	srv, err := sheets.NewService(ctx, option.WithCredentialsFile(credentialsPath), option.WithScopes(
		"https://www.googleapis.com/auth/spreadsheets",
		"https://www.googleapis.com/auth/drive",
	))

	if err != nil {
		slog.Error("Failed to connect to Google Sheet", "error", err)
		panic(err)
	}

	slog.Info("Successfully connected to Google Sheet.")

	return &GoogleSheetService{
		srv:           srv,
		spreadsheetID: spID,
		cache:         make(map[string]cacheEntry),
		cacheTTL:      time.Duration(cacheTTLSeconds) * time.Second,
	}
}

func (g *GoogleSheetService) GetData(category string) []map[string]interface{} {
	g.mu.RLock()
	if entry, exists := g.cache[category]; exists {
		if time.Since(entry.timestamp) < g.cacheTTL {
			g.mu.RUnlock()
			return entry.data
		}
	}
	g.mu.RUnlock()

	if g.srv == nil {
		slog.Error("Google Sheet not available.")
		return []map[string]interface{}{}
	}

	ctx := context.Background()
	resp, err := g.srv.Spreadsheets.Values.Get(g.spreadsheetID, category).Context(ctx).Do()

	if err != nil {
		slog.Error(fmt.Sprintf("Failed to get data from worksheet '%s'", category), "error", err)
		return []map[string]interface{}{}
	}

	if len(resp.Values) == 0 {
		slog.Error(fmt.Sprintf("Worksheet '%s' not found.", category))
		return []map[string]interface{}{}
	}

	headers := resp.Values[0]
	var data []map[string]interface{}

	for _, row := range resp.Values[1:] {
		record := make(map[string]interface{})
		for i, header := range headers {
			key := fmt.Sprintf("%v", header)
			if i < len(row) {
				valStr := strings.TrimSpace(fmt.Sprintf("%v", row[i]))
				if floatVal, err := strconv.ParseFloat(strings.ReplaceAll(valStr, ",", ""), 64); err == nil {
					record[key] = floatVal
				} else {
					record[key] = valStr
				}
			} else {
				record[key] = ""
			}
		}
		data = append(data, record)
	}

	g.mu.Lock()
	g.cache[category] = cacheEntry{
		data:      data,
		timestamp: time.Now(),
	}
	g.mu.Unlock()

	slog.Info(fmt.Sprintf("Fetched and cached data for worksheet: %s", category))
	return data
}

type WhatsAppBridgeService struct {
	client *whatsmeow.Client
}

func NewWhatsAppBridgeService(client *whatsmeow.Client) *WhatsAppBridgeService {
	return &WhatsAppBridgeService{
		client: client,
	}
}

func (w *WhatsAppBridgeService) SendMessage(ctx context.Context, recipient string, message string) error {
	var jid types.JID
	if strings.Contains(recipient, "@") {
		var err error
		jid, err = types.ParseJID(recipient)
		if err != nil {
			return err
		}
	} else {
		cleanNumber := strings.TrimPrefix(recipient, "+")
		jid = types.NewJID(cleanNumber, types.DefaultUserServer)
	}

	msg := &waProto.Message{Conversation: proto.String(message)}

	var sendErr error
	delay := 2 * time.Second

	for attempt := 1; attempt <= 3; attempt++ {
		_, sendErr = w.client.SendMessage(ctx, jid, msg)
		if sendErr == nil {
			slog.Info("Message sent successfully", "recipient", recipient)
			return nil
		}

		slog.Error("Failed to send message via API", "to", recipient, "error", sendErr.Error())

		if attempt < 3 {
			time.Sleep(delay)
			delay *= 2
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
		}
	}

	return sendErr
}

type OpenAIService struct {
	client *openai.Client
	model  string
}

func NewOpenAIService(apiKey string, model string) *OpenAIService {
	return &OpenAIService{
		client: openai.NewClient(apiKey),
		model:  model,
	}
}

func (o *OpenAIService) CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return o.client.CreateChatCompletion(ctx, req)
}

func (o *OpenAIService) GetAIResponse(ctx context.Context, messages []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionResponse, error) {
	req := openai.ChatCompletionRequest{
		Model:       o.model,
		Messages:    messages,
		Tools:       tools,
		Temperature: 0.0,
		MaxTokens:   400,
	}
	return o.client.CreateChatCompletion(ctx, req)
}
