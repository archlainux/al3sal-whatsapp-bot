import asyncio
import secrets
from contextlib import asynccontextmanager

import asyncpg
import structlog
from fastapi import BackgroundTasks
from fastapi import Depends
from fastapi import FastAPI
from fastapi import Header
from fastapi import HTTPException
from fastapi import Request
from pydantic import BaseModel
from structlog.dev import ConsoleRenderer

from app.bot_logic import ConversationManager
from app.config import settings
from app.database import DatabaseService
from app.database import initialize_database
from app.services import GoogleSheetService
from app.services import OpenAIService
from app.services import WhatsAppBridgeService

logger = structlog.get_logger(__name__)


class WebhookRequest(BaseModel):
    from_number: str
    body: str


class ResumeRequest(BaseModel):
    user_number: str


class PauseRequest(BaseModel):
    user_number: str


structlog.configure(processors=[structlog.contextvars.merge_contextvars, structlog.stdlib.add_logger_name, structlog.stdlib.add_log_level, structlog.processors.TimeStamper(fmt="iso"), ConsoleRenderer()], context_class=dict, logger_factory=structlog.stdlib.LoggerFactory(), wrapper_class=structlog.BoundLogger, cache_logger_on_first_use=True)


async def run_periodic_cleanup(db_service: DatabaseService):
    cleanup_interval_seconds = settings.CLEANUP_INTERVAL_HOURS * 3600
    while True:
        await asyncio.sleep(cleanup_interval_seconds)
        try:
            deleted_count = await db_service.cleanup_old_messages(settings.MESSAGE_HISTORY_TTL_DAYS)
            logger.info("Periodic cleanup finished.", deleted_count=deleted_count)
        except Exception as e:
            logger.error("Periodic cleanup task failed.", error=str(e), exc_info=True)


@asynccontextmanager
async def lifespan(app: FastAPI):
    db_pool = await asyncpg.create_pool(settings.DATABASE_URL)
    await initialize_database(db_pool)

    db_service = DatabaseService(db_pool)
    whatsapp_service = WhatsAppBridgeService(settings)
    sheet_service = GoogleSheetService(settings.GOOGLE_CREDENTIALS_PATH, settings.GOOGLE_SHEET_URL)
    openai_service = OpenAIService(settings)
    app.state.manager = ConversationManager(db_service, whatsapp_service, sheet_service, openai_service, settings)

    asyncio.create_task(run_periodic_cleanup(db_service))
    logger.info("Application startup complete. Services initialized.")
    yield
    await db_pool.close()
    logger.info("Application shutdown complete. Resources closed.")


app = FastAPI(title="Al3sal OpenAI WhatsApp Bot", lifespan=lifespan)


def get_manager(request: Request) -> ConversationManager:
    return request.app.state.manager


async def verify_admin_api_key(x_admin_api_key: str = Header(...)):
    if not secrets.compare_digest(x_admin_api_key, settings.ADMIN_API_KEY):
        raise HTTPException(status_code=401, detail="Invalid or missing Admin API Key")


async def verify_bridge_api_key(x_api_key: str = Header(..., alias="X-API-Key")):
    if not secrets.compare_digest(x_api_key, settings.INTERNAL_API_KEY):
        raise HTTPException(status_code=401, detail="Invalid or missing API Key for internal service")


@app.post("/webhook", tags=["Bot Webhook"], dependencies=[Depends(verify_bridge_api_key)])
async def bot_webhook(data: WebhookRequest, background_tasks: BackgroundTasks, manager: ConversationManager = Depends(get_manager)):
    if not data.from_number or not data.body:
        raise HTTPException(status_code=400, detail="Missing sender or message body")
    background_tasks.add_task(manager.handle_incoming_message, data.from_number, data.body)
    return {"status": "ok"}


@app.post("/internal/resume", tags=["Internal Actions"], dependencies=[Depends(verify_bridge_api_key)])
async def resume_bot_for_user(data: ResumeRequest, manager: ConversationManager = Depends(get_manager)):
    await manager.resume_bot_for_user(data.user_number)
    return {"status": f"Bot resumed for {data.user_number}"}


@app.post("/internal/pause", tags=["Internal Actions"], dependencies=[Depends(verify_bridge_api_key)])
async def pause_bot_for_user(data: PauseRequest, manager: ConversationManager = Depends(get_manager)):
    await manager.pause_bot_for_user(data.user_number)
    return {"status": f"Bot paused for {data.user_number}"}


@app.get("/admin/states", tags=["Admin Actions"], dependencies=[Depends(verify_admin_api_key)])
async def get_all_user_states(db: DatabaseService = Depends(lambda r: r.app.state.manager.db)):
    async with db.pool.acquire() as conn:
        states = await conn.fetch("SELECT sender_id, state, context::text, updated_at FROM conversation_state ORDER BY updated_at DESC;")
        return [dict(state) for state in states]


@app.get("/health", tags=["Health"])
async def health_check():
    return {"status": "ok"}
