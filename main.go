package main

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"app/bot"
	"app/config"
	"app/database"
	"app/services"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

var manager *bot.ConversationManager
var appSettings *config.Settings

var botStartTime = time.Now()

func secureCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func runPeriodicCleanup(ctx context.Context, dbService *database.DatabaseService, settings *config.Settings) {
	ticker := time.NewTicker(time.Duration(settings.CleanupIntervalHours) * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			deletedCount := dbService.CleanupOldMessages(context.Background(), settings.MessageHistoryTTLDays)
			slog.Info("Periodic cleanup finished.", "deleted_count", deletedCount)
		case <-ctx.Done():
			slog.Info("Stopping periodic cleanup task.")
			return
		}
	}
}

func verifyAdminAPIKey(settings *config.Settings) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("x-admin-api-key")
		if !secureCompare(key, settings.AdminAPIKey) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"detail": "Invalid or missing Admin API Key"})
			return
		}
		c.Next()
	}
}

func verifyBridgeAPIKey(settings *config.Settings) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("X-API-Key")
		if !secureCompare(key, settings.InternalAPIKey) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"detail": "Invalid or missing API Key for internal service"})
			return
		}
		c.Next()
	}
}

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Connected:
		slog.Info("WhatsApp client is ready!")
	case *events.LoggedOut:
		slog.Warn("Client was logged out", "reason", v.Reason)
	case *events.Message:
		if v.Info.Timestamp.Before(botStartTime) {
			return
		}

		if v.Info.Chat.String() == "status@broadcast" {
			return
		}

		body := v.Message.GetConversation()
		if body == "" {
			body = v.Message.GetExtendedTextMessage().GetText()
		}

		senderID := v.Info.Chat.String()

		if v.Info.IsFromMe {
			if body == "" {
				return
			}
			command := strings.ToLower(strings.TrimSpace(body))

			if command == strings.ToLower(appSettings.BotPauseCommand) {
				slog.Info("Pause command detected", "user", senderID)
				manager.PauseBotForUser(context.Background(), senderID)
			} else if command == strings.ToLower(appSettings.BotResumeCommand) {
				slog.Info("Resume command detected", "user", senderID)
				manager.ResumeBotForUser(context.Background(), senderID)
			}
			return
		}

		if body != "" {
			contextInfo := v.Message.GetExtendedTextMessage().GetContextInfo()
			if contextInfo != nil && contextInfo.GetStanzaID() != "" {
				if contextInfo.GetParticipant() == "status@broadcast" || contextInfo.GetRemoteJID() == "status@broadcast" {
					return
				}
			}

			slog.Info("Received message", "from", senderID, "body", body)
			go manager.HandleIncomingMessage(context.Background(), senderID, body)
		}
	}
}

func main() {
	logLevel := os.Getenv("LOG_LEVEL")
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(handler))

	appSettings = config.LoadSettings()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbPool, err := pgxpool.New(ctx, appSettings.DatabaseURL)
	if err != nil {
		slog.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}

	err = database.InitializeDatabase(ctx, dbPool)
	if err != nil {
		slog.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}

	dbService := database.NewDatabaseService(dbPool)

	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(ctx, "sqlite3", "file:data/wa.db?_foreign_keys=on", dbLog)
	if err != nil {
		slog.Error("Failed to initialize local database", "error", err)
		os.Exit(1)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		slog.Error("Failed to get device", "error", err)
		os.Exit(1)
	}

	clientLog := waLog.Stdout("Client", "ERROR", true)
	waClient := whatsmeow.NewClient(deviceStore, clientLog)
	waClient.AddEventHandler(eventHandler)

	if waClient.Store.ID == nil {
		qrChan, _ := waClient.GetQRChannel(context.Background())
		err = waClient.Connect()
		if err != nil {
			slog.Error("Failed to connect", "error", err)
			os.Exit(1)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				slog.Info("QR Code Received, scan it with your phone.")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			}
		}
	} else {
		err = waClient.Connect()
		if err != nil {
			slog.Error("Failed to connect", "error", err)
			os.Exit(1)
		}
	}

	whatsappService := services.NewWhatsAppBridgeService(waClient)
	sheetService := services.NewGoogleSheetService(appSettings.GoogleCredentialsPath, appSettings.GoogleSheetURL)
	openaiService := services.NewOpenAIService(appSettings.OpenAIAPIKey, appSettings.ChatModel)

	manager = bot.NewConversationManager(dbService, whatsappService, sheetService, openaiService, appSettings)

	go runPeriodicCleanup(ctx, dbService, appSettings)
	slog.Info("Application startup complete. Services initialized.")

	gin.SetMode(gin.ReleaseMode)
	app := gin.Default()

	type UserRequest struct {
		UserNumber string `json:"user_number" binding:"required"`
	}

	app.POST("/internal/resume", verifyBridgeAPIKey(appSettings), func(c *gin.Context) {
		var req UserRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		manager.ResumeBotForUser(context.Background(), req.UserNumber)
		c.JSON(http.StatusOK, gin.H{"status": "Bot resumed for " + req.UserNumber})
	})

	app.POST("/internal/pause", verifyBridgeAPIKey(appSettings), func(c *gin.Context) {
		var req UserRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		manager.PauseBotForUser(context.Background(), req.UserNumber)
		c.JSON(http.StatusOK, gin.H{"status": "Bot paused for " + req.UserNumber})
	})

	app.GET("/admin/states", verifyAdminAPIKey(appSettings), func(c *gin.Context) {
		rows, err := dbPool.Query(context.Background(), "SELECT sender_id, state, context::text, updated_at FROM conversation_state ORDER BY updated_at DESC;")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Database error"})
			return
		}
		defer rows.Close()

		states := make([]map[string]interface{}, 0)
		for rows.Next() {
			var senderID, state, contextStr string
			var updatedAt time.Time
			if err := rows.Scan(&senderID, &state, &contextStr, &updatedAt); err == nil {
				states = append(states, map[string]interface{}{
					"sender_id":  senderID,
					"state":      state,
					"context":    contextStr,
					"updated_at": updatedAt,
				})
			}
		}
		c.JSON(http.StatusOK, states)
	})

	app.GET("/health", func(c *gin.Context) {
		isConnected := waClient.IsConnected()
		if isConnected {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "whatsapp_state": "CONNECTED"})
		} else {
			slog.Warn("Health check failed: WhatsApp not connected", "state", "DISCONNECTED")
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "whatsapp_state": "DISCONNECTED"})
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: app,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to listen and serve", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("Shutting down gracefully...")

	cancel()

	ctxShutdown, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(ctxShutdown); err != nil {
		slog.Error("Error destroying client during shutdown.", "error", err)
	}
	slog.Info("HTTP server closed.")

	waClient.Disconnect()
	slog.Info("WhatsApp client destroyed.")

	dbPool.Close()
	slog.Info("Application shutdown complete. Resources closed.")
}
