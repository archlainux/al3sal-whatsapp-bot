import json
from typing import Any, Dict, List

import asyncpg

from app.models import UserSession


class DatabaseService:
    def __init__(self, pool: asyncpg.Pool):
        self.pool = pool

    async def add_message_to_history(self, sender_id: str, role: str, content: str):
        async with self.pool.acquire() as conn:
            await conn.execute("INSERT INTO message_history (sender_id, role, content) VALUES ($1, $2, $3)", sender_id, role, content)

    async def cleanup_old_messages(self, ttl_days: int) -> int:
        async with self.pool.acquire() as conn:
            result = await conn.execute("DELETE FROM message_history WHERE timestamp < NOW() - $1::interval", f"{ttl_days} days")
            try:
                deleted_count = int(result.split()[-1])
            except (ValueError, IndexError):
                deleted_count = 0
            return deleted_count

    async def get_last_user_message_content(self, sender_id: str) -> str:
        async with self.pool.acquire() as conn:
            return await conn.fetchval("SELECT content FROM message_history WHERE sender_id = $1 AND role = 'user' ORDER BY timestamp DESC LIMIT 1", sender_id)

    async def get_recent_messages(self, sender_id: str, limit: int) -> List[Dict]:
        async with self.pool.acquire() as conn:
            rows = await conn.fetch("SELECT role, content FROM message_history WHERE sender_id = $1 ORDER BY timestamp DESC LIMIT $2", sender_id, limit)
            return list(reversed([dict(row) for row in rows]))

    async def get_user_session(self, sender_id: str) -> UserSession:
        async with self.pool.acquire() as conn:
            await conn.execute(
                "INSERT INTO conversation_state (sender_id, state, context) VALUES ($1, 'bot', '{}') ON CONFLICT (sender_id) DO NOTHING",
                sender_id
            )
            record = await conn.fetchrow("SELECT state, context FROM conversation_state WHERE sender_id = $1", sender_id)
            return UserSession(state=record['state'], context=json.loads(record['context']) if record['context'] else {})

    async def update_user_session(self, sender_id: str, session: UserSession):
        async with self.pool.acquire() as conn:
            await conn.execute(
                "INSERT INTO conversation_state (sender_id, state, context) VALUES ($1, $2, $3) "
                "ON CONFLICT (sender_id) DO UPDATE SET state = $2, context = $3, updated_at = NOW()",
                sender_id, session.state, json.dumps(session.context)
            )


async def initialize_database(pool: asyncpg.Pool):
    async with pool.acquire() as conn:
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS conversation_state (
                sender_id VARCHAR(255) PRIMARY KEY,
                state VARCHAR(50) NOT NULL DEFAULT 'bot',
                context JSONB NOT NULL DEFAULT '{}'::jsonb,
                created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
            );
        """)
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS message_history (
                id SERIAL PRIMARY KEY,
                sender_id VARCHAR(255) NOT NULL,
                role VARCHAR(50) NOT NULL,
                content TEXT NOT NULL,
                timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW()
            );
        """)
