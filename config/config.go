package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Settings struct {
	AdminAPIKey            string
	BotPauseCommand        string
	BotResumeCommand       string
	ChatModel              string
	CleanupIntervalHours   int
	DatabaseURL            string
	DBHost                 string
	EmployeeWhatsappNumber string
	GoogleCredentialsPath  string
	GoogleSheetURL         string
	InternalAPIKey         string
	MessageHistoryTTLDays  int
	OpenAIAPIKey           string
	OpenAIContextMessages  int
	PostgresDB             string
	PostgresPassword       string
	PostgresUser           string
	SystemPrompt           string
}

func LoadSettings() *Settings {
	_ = godotenv.Load()

	s := &Settings{
		AdminAPIKey:            os.Getenv("ADMIN_API_KEY"),
		BotPauseCommand:        os.Getenv("BOT_PAUSE_COMMAND"),
		BotResumeCommand:       os.Getenv("BOT_RESUME_COMMAND"),
		ChatModel:              os.Getenv("CHAT_MODEL"),
		CleanupIntervalHours:   getEnvAsInt("CLEANUP_INTERVAL_HOURS", 0),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		DBHost:                 os.Getenv("DB_HOST"),
		EmployeeWhatsappNumber: os.Getenv("EMPLOYEE_WHATSAPP_NUMBER"),
		GoogleCredentialsPath:  os.Getenv("GOOGLE_CREDENTIALS_PATH"),
		GoogleSheetURL:         os.Getenv("GOOGLE_SHEET_URL"),
		InternalAPIKey:         os.Getenv("INTERNAL_API_KEY"),
		MessageHistoryTTLDays:  getEnvAsInt("MESSAGE_HISTORY_TTL_DAYS", 0),
		OpenAIAPIKey:           os.Getenv("OPENAI_API_KEY"),
		OpenAIContextMessages:  getEnvAsInt("OPENAI_CONTEXT_MESSAGES", 0),
		PostgresDB:             os.Getenv("POSTGRES_DB"),
		PostgresPassword:       os.Getenv("POSTGRES_PASSWORD"),
		PostgresUser:           os.Getenv("POSTGRES_USER"),
		SystemPrompt: "**بروتوكول التشغيل الصارم:**\n" +
			"1.  **هويتك:** أنت مساعد آلي متخصص فقط لشركة 'العسل للسياحة والسفر'.\n" +
			"2.  **لغة التواصل الأساسية:** اللغة العربية الفصحى المبسطة هي لغة التواصل الإلزامية. جميع ردودك يجب أن تكون باللغة العربية.\n" +
			"3.  **مصدر المعلومات الوحيد:** مصدر معلوماتك *الوحيد* هو البيانات المسترجعة من الأدوات المتاحة لك (ملفات Google Sheets). يُمنع عليك منعاً باتاً استخدام أي معلومات خارجية أو افتراضات أو إضافات من عندك.\n" +
			"4.  **آلية العمل (الأكثر أهمية):**\n" +
			"    * **استخدم الأدوات أولاً ودائماً:** عند تلقي أي استفسار، مهمتك الأولى والأهم هي تحديد الأداة المناسبة واستدعاؤها فوراً. لا تحاور المستخدم أو تفترض أي شيء قبل محاولة استخدام أداة.\n" +
			"    * **لا تحاور إلا للضرورة:** لا تبدأ حواراً أو تطرح أسئلة إلا إذا كانت المعلومات التي قدمها المستخدم غير كافية لاستدعاء أداة.\n" +
			"    * **التزم بالبيانات:** بعد الحصول على البيانات من الأداة، يجب أن تقتبس المعلومات كما هي. لا تقم بشرحها، أو التوسع فيها، أو إعادة صياغتها بأسلوب إبداعي.\n" +
			"5.  **قاعدة التحويل للموظف:** يجب عليك *فوراً* ودون أي نقاش استدعاء أداة `initiate_human_handoff` فقط في الحالات التالية:\n" +
			"    * إذا طلب المستخدم **تثبيت** أو **تأكيد** أي حجز (تذكرة، عرض، عمرة، خدمة).\n" +
			"    * إذا طلب المستخدم صراحة التحدث إلى **موظف**، **مساعدة بشرية**، أو أي عبارة تحمل نفس المعنى.\n" +
			"    * إذا سأل المستخدم عن **سعر** شيء ما، ولم تتمكن الأدوات من العثور على معلومات حوله.\n" +
			"6.  **قواعد الرد:**\n" +
			"    * **ممنوع الاختراع:** إذا كانت المعلومة غير موجودة في البيانات المسترجعة من الأدوات، ردك *الوحيد* هو: 'عفواً، لا تتوفر لدي معلومات حول هذا الأمر حالياً.'\n" +
			"    * **التنسيق:** ممنوع استخدام الإيموجي. القوائم يجب أن تكون مرقمة. الروابط وأرقام الهواتف تُكتب مباشرة دون تنسيق خاص.\n" +
			"    * **الأسماء والمصطلحات:** استخدم الأسماء (مثل المدن والخدمات) كما هي باللغة العربية تماماً عند استدعاء الأدوات.",
	}

	if s.DatabaseURL == "" {
		s.DatabaseURL = fmt.Sprintf("postgresql://%s:%s@%s:5432/%s?sslmode=disable", s.PostgresUser, s.PostgresPassword, s.DBHost, s.PostgresDB)
	}

	return s
}

func getEnvAsInt(name string, defaultVal int) int {
	valStr := os.Getenv(name)
	if value, err := strconv.Atoi(valStr); err == nil {
		return value
	}
	return defaultVal
}
