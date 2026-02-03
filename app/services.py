import time
from typing import Dict
from typing import List

import aiohttp
import gspread
import openai
import structlog
from google.oauth2.service_account import Credentials
from tenacity import retry
from tenacity import retry_if_exception_type
from tenacity import stop_after_attempt
from tenacity import wait_exponential

from app.config import Settings

logger = structlog.get_logger(__name__)


class GoogleSheetService:
    def __init__(self, credentials_path: str, sheet_url: str, cache_ttl_seconds: int = 300):
        self.sheet = None
        self.cache = {}
        self.cache_ttl = cache_ttl_seconds
        try:
            scopes = ["https://www.googleapis.com/auth/spreadsheets", "https://www.googleapis.com/auth/drive"]
            creds = Credentials.from_service_account_file(credentials_path, scopes=scopes)
            self.client = gspread.authorize(creds)
            self.sheet = self.client.open_by_url(sheet_url)
            logger.info("Successfully connected to Google Sheet.")
        except Exception as e:
            logger.error("Failed to connect to Google Sheet", error=str(e))
            raise

    def _get_worksheet_data(self, worksheet_name: str, force_refresh: bool = False) -> List[Dict]:
        current_time = time.time()
        if not force_refresh and worksheet_name in self.cache:
            data, timestamp = self.cache[worksheet_name]
            if current_time - timestamp < self.cache_ttl:
                return data
        if not self.sheet:
            logger.error("Google Sheet not available.")
            return []
        try:
            worksheet = self.sheet.worksheet(worksheet_name)
            data = worksheet.get_all_records()
            self.cache[worksheet_name] = (data, current_time)
            logger.info(f"Fetched and cached data for worksheet: {worksheet_name}")
            return data
        except gspread.exceptions.WorksheetNotFound:
            logger.error(f"Worksheet '{worksheet_name}' not found.")
            return []
        except Exception as e:
            logger.error(f"Failed to get data from worksheet '{worksheet_name}'", error=str(e))
            return []

    def get_data(self, category: str) -> List[Dict]:
        return self._get_worksheet_data(category)


class WhatsAppBridgeService:
    def __init__(self, settings: Settings):
        self.url = f"{settings.BRIDGE_URL}/send-message"
        self.headers = {'Content-Type': 'application/json', 'X-API-Key': settings.INTERNAL_API_KEY}

    @retry(stop=stop_after_attempt(3), wait=wait_exponential(multiplier=1, min=2, max=10), retry=retry_if_exception_type(aiohttp.ClientError), reraise=True)
    async def send_message(self, number: str, message: str):
        payload = {"number": number, "message": message}
        async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=15)) as session:
            async with session.post(self.url, json=payload, headers=self.headers) as response:
                response.raise_for_status()
                logger.info("Message sent to bridge successfully", recipient=payload.get('number'))


class OpenAIService:
    def __init__(self, settings: Settings):
        self.client = openai.AsyncOpenAI(api_key=settings.OPENAI_API_KEY)
        self.model = settings.CHAT_MODEL

    async def get_ai_response(self, messages: List[Dict], tools: List[Dict]):
        return await self.client.chat.completions.create(
            model=self.model,
            messages=messages,
            tools=tools,
            tool_choice="auto",
            temperature=0.0,
            max_tokens=400
        )
