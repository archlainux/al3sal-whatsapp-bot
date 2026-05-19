package database

import (
	"context"
	"encoding/json"
	"fmt"

	"app/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sashabaranov/go-openai"
)

type DatabaseService struct {
	pool *pgxpool.Pool
}

func NewDatabaseService(pool *pgxpool.Pool) *DatabaseService {
	return &DatabaseService{pool: pool}
}

func (db *DatabaseService) AddMessageToHistory(ctx context.Context, senderID string, role string, content string) error {
	_, err := db.pool.Exec(ctx, "INSERT INTO message_history (sender_id, role, content) VALUES ($1, $2, $3)", senderID, role, content)
	return err
}

func (db *DatabaseService) CleanupOldMessages(ctx context.Context, ttlDays int) int {
	interval := fmt.Sprintf("%d days", ttlDays)
	tag, err := db.pool.Exec(ctx, "DELETE FROM message_history WHERE timestamp < NOW() - $1::interval", interval)
	if err != nil {
		return 0
	}
	return int(tag.RowsAffected())
}

func (db *DatabaseService) GetLastUserMessageContent(ctx context.Context, senderID string) string {
	var content string
	err := db.pool.QueryRow(ctx, "SELECT content FROM message_history WHERE sender_id = $1 AND role = 'user' ORDER BY timestamp DESC LIMIT 1", senderID).Scan(&content)
	if err != nil {
		return ""
	}
	return content
}

func (db *DatabaseService) GetRecentMessages(ctx context.Context, senderID string, limit int) []openai.ChatCompletionMessage {
	rows, err := db.pool.Query(ctx, "SELECT role, content FROM message_history WHERE sender_id = $1 ORDER BY timestamp DESC LIMIT $2", senderID, limit)
	if err != nil {
		return []openai.ChatCompletionMessage{}
	}
	defer rows.Close()

	var messages []openai.ChatCompletionMessage
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err == nil {
			messages = append(messages, openai.ChatCompletionMessage{Role: role, Content: content})
		}
	}

	if err := rows.Err(); err != nil {
		return []openai.ChatCompletionMessage{}
	}

	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages
}

func (db *DatabaseService) GetUserSession(ctx context.Context, senderID string) models.UserSession {
	_, _ = db.pool.Exec(ctx, "INSERT INTO conversation_state (sender_id, state, context) VALUES ($1, 'bot', '{}'::jsonb) ON CONFLICT (sender_id) DO NOTHING", senderID)

	var state string
	var contextBytes []byte
	err := db.pool.QueryRow(ctx, "SELECT state, context FROM conversation_state WHERE sender_id = $1", senderID).Scan(&state, &contextBytes)

	session := models.NewUserSession()

	if err == nil {
		session.State = state
		if len(contextBytes) > 0 {
			json.Unmarshal(contextBytes, &session.Context)
		}
	}
	return session
}

func (db *DatabaseService) UpdateUserSession(ctx context.Context, senderID string, session models.UserSession) error {
	contextBytes, _ := json.Marshal(session.Context)
	_, err := db.pool.Exec(ctx,
		"INSERT INTO conversation_state (sender_id, state, context) VALUES ($1, $2, $3::jsonb) "+
			"ON CONFLICT (sender_id) DO UPDATE SET state = $2, context = $3::jsonb, updated_at = NOW()",
		senderID, session.State, contextBytes)
	return err
}

func InitializeDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
            CREATE TABLE IF NOT EXISTS conversation_state (
                sender_id VARCHAR(255) PRIMARY KEY,
                state VARCHAR(50) NOT NULL DEFAULT 'bot',
                context JSONB NOT NULL DEFAULT '{}'::jsonb,
                created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
            );
        `)
	if err != nil {
		return err
	}

	_, err = pool.Exec(ctx, `
            CREATE TABLE IF NOT EXISTS message_history (
                id SERIAL PRIMARY KEY,
                sender_id VARCHAR(255) NOT NULL,
                role VARCHAR(50) NOT NULL,
                content TEXT NOT NULL,
                timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW()
            );
        `)
	return err
}
