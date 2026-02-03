from typing import Any, Dict
from pydantic import BaseModel


class UserSession(BaseModel):
    state: str = 'bot'
    context: Dict[str, Any] = {}
