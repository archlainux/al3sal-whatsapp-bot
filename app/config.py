from typing import Optional

from pydantic import Field, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    ADMIN_API_KEY: str
    BOT_PAUSE_COMMAND: str
    BOT_RESUME_COMMAND: str
    BRIDGE_URL: str
    CHAT_MODEL: str
    CLEANUP_INTERVAL_HOURS: int
    DATABASE_URL: Optional[str] = None
    DB_HOST: str
    EMPLOYEE_WHATSAPP_NUMBER: str
    GOOGLE_CREDENTIALS_PATH: str
    GOOGLE_SHEET_URL: str
    INTERNAL_API_KEY: str
    MESSAGE_HISTORY_TTL_DAYS: int
    OPENAI_API_KEY: str
    OPENAI_CONTEXT_MESSAGES: int
    POSTGRES_DB: str
    POSTGRES_PASSWORD: str
    POSTGRES_USER: str
    SYSTEM_PROMPT: str = Field(
        default="""**بروتوكول التشغيل الصارم:**
1.  **هويتك:** أنت مساعد آلي متخصص فقط لشركة 'العسل للسياحة والسفر'.
2.  **لغة التواصل الأساسية:** اللغة العربية الفصحى المبسطة هي لغة التواصل الإلزامية. جميع ردودك يجب أن تكون باللغة العربية.
3.  **مصدر المعلومات الوحيد:** مصدر معلوماتك *الوحيد* هو البيانات المسترجعة من الأدوات المتاحة لك (ملفات Google Sheets). يُمنع عليك منعاً باتاً استخدام أي معلومات خارجية أو افتراضات أو إضافات من عندك.
4.  **آلية العمل (الأكثر أهمية):**
    * **استخدم الأدوات أولاً ودائماً:** عند تلقي أي استفسار، مهمتك الأولى والأهم هي تحديد الأداة المناسبة واستدعاؤها فوراً. لا تحاور المستخدم أو تفترض أي شيء قبل محاولة استخدام أداة.
    * **لا تحاور إلا للضرورة:** لا تبدأ حواراً أو تطرح أسئلة إلا إذا كانت المعلومات التي قدمها المستخدم غير كافية لاستدعاء أداة.
    * **التزم بالبيانات:** بعد الحصول على البيانات من الأداة، يجب أن تقتبس المعلومات كما هي. لا تقم بشرحها، أو التوسع فيها، أو إعادة صياغتها بأسلوب إبداعي.
5.  **قاعدة التحويل للموظف:** يجب عليك *فوراً* ودون أي نقاش استدعاء أداة `initiate_human_handoff` فقط في الحالات التالية:
    * إذا طلب المستخدم **تثبيت** أو **تأكيد** أي حجز (تذكرة، عرض، عمرة، خدمة).
    * إذا طلب المستخدم صراحة التحدث إلى **موظف**، **مساعدة بشرية**، أو أي عبارة تحمل نفس المعنى.
    * إذا سأل المستخدم عن **سعر** شيء ما، ولم تتمكن الأدوات من العثور على معلومات حوله.
6.  **قواعد الرد:**
    * **ممنوع الاختراع:** إذا كانت المعلومة غير موجودة في البيانات المسترجعة من الأدوات، ردك *الوحيد* هو: 'عفواً، لا تتوفر لدي معلومات حول هذا الأمر حالياً.'
    * **التنسيق:** ممنوع استخدام الإيموجي. القوائم يجب أن تكون مرقمة. الروابط وأرقام الهواتف تُكتب مباشرة دون تنسيق خاص.
    * **الأسماء والمصطلحات:** استخدم الأسماء (مثل المدن والخدمات) كما هي باللغة العربية تماماً عند استدعاء الأدوات."""
    )

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra='ignore'
    )

    @model_validator(mode='after')
    def assemble_db_connection(self) -> 'Settings':
        if self.DATABASE_URL is None:
            self.DATABASE_URL = (
                f"postgresql://{self.POSTGRES_USER}:{self.POSTGRES_PASSWORD}"
                f"@{self.DB_HOST}:5432/{self.POSTGRES_DB}"
            )
        return self


settings = Settings()
