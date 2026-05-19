package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"app/config"
	"app/database"
	"app/models"
	"app/services"
	"github.com/sashabaranov/go-openai"
)

var logger = slog.Default()

var (
	arabicRegex1    = regexp.MustCompile(`[إأآا]`)
	arabicRegex2    = regexp.MustCompile(`ى`)
	arabicRegex3    = regexp.MustCompile(`ة`)
	arabicTashkeel  = regexp.MustCompile(`[\x{064B}-\x{0652}]`)
	arabicLangMatch = regexp.MustCompile(`[\p{Arabic}]`)
	emojiPattern    = regexp.MustCompile(`[\x{1F600}-\x{1F64F}\x{1F300}-\x{1F5FF}\x{1F680}-\x{1F6FF}\x{1F1E0}-\x{1F1FF}\x{2702}-\x{27B0}\x{24C2}-\x{1F251}]+`)
	arabicNumeralsReplacer = strings.NewReplacer(
		"٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4",
		"٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9",
		"۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4",
		"۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9",
	)
)

type ConversationManager struct {
	db                             *database.DatabaseService
	whatsapp                       *services.WhatsAppBridgeService
	sheets                         *services.GoogleSheetService
	openai                         *services.OpenAIService
	settings                       *config.Settings
	SYSTEM_PROMPT                  string
	EMPLOYEE_NOTIFICATION_TEMPLATE string
	ROUTINE_RESPONSES              map[string]string
}

func NewConversationManager(dbService *database.DatabaseService, whatsappService *services.WhatsAppBridgeService, sheetService *services.GoogleSheetService, openaiService *services.OpenAIService, settings *config.Settings) *ConversationManager {
	routineResponses := map[string]string{
		"شكرا":         "على الرحب والسعة!",
		"مشكور":        "على الرحب والسعة!",
		"يسلمو":        "على الرحب والسعة!",
		"مرحبا":        "أهلاً بك. كيف يمكنني خدمتك؟",
		"هلا":          "أهلاً بك. كيف يمكنني خدمتك؟",
		"السلام عليكم": "أهلاً بك. كيف يمكنني خدمتك؟",
		"تمام":         "بالخدمة.",
		"اوك":          "بالخدمة.",
		"ك":            "بالخدمة.",
	}

	return &ConversationManager{
		db:                             dbService,
		whatsapp:                       whatsappService,
		sheets:                         sheetService,
		openai:                         openaiService,
		settings:                       settings,
		SYSTEM_PROMPT:                  settings.SystemPrompt,
		EMPLOYEE_NOTIFICATION_TEMPLATE: "*تنبيه: مطلوب تدخل بشري*\n\nالعميل `{customer_id}` بحاجة إلى مساعدة.\n\n*السبب:* {reason}\n\nيرجى فتح واتساب والتواصل معه.",
		ROUTINE_RESPONSES:              routineResponses,
	}
}

func (c *ConversationManager) detectLanguage(text string) string {
	if arabicLangMatch.MatchString(text) {
		return "ar"
	}
	return "en"
}

func (c *ConversationManager) flightFormatter(item map[string]interface{}) string {
	origin := c.getStringDefault(item, "depart_airport", "N/A")
	destination := c.getStringDefault(item, "destination_airport", "N/A")
	date := c.getStringDefault(item, "depart_date", "N/A")
	flightInfo := fmt.Sprintf("رحلة من %s إلى %s | بتاريخ %s", origin, destination, date)
	price := c.getString(item, "usd_price")
	if price != "" {
		flightInfo += fmt.Sprintf(" | السعر: %s$", price)
	}
	return flightInfo
}

func (c *ConversationManager) formatFlightDetails(item map[string]interface{}) string {
	titleType := c.getStringDefault(item, "type", "رحلة")
	titleDest := c.getString(item, "destination_airport")
	title := fmt.Sprintf("*%s إلى %s*", titleType, titleDest)
	parts := []string{title}

	if c.hasValue(item, "depart_airport") && c.hasValue(item, "destination_airport") {
		parts = append(parts, fmt.Sprintf("• *المسار:* من %s إلى %s", c.getString(item, "depart_airport"), c.getString(item, "destination_airport")))
	}
	if c.hasValue(item, "depart_date") {
		parts = append(parts, fmt.Sprintf("• *تاريخ الإقلاع:* %s", c.getString(item, "depart_date")))
	}
	if c.hasValue(item, "return_date") {
		parts = append(parts, fmt.Sprintf("• *تاريخ العودة:* %s", c.getString(item, "return_date")))
	}
	if c.hasValue(item, "time_of_depart") {
		parts = append(parts, fmt.Sprintf("• *وقت الإقلاع:* %s", c.getString(item, "time_of_depart")))
	}
	if c.hasValue(item, "time_of_arrival") {
		parts = append(parts, fmt.Sprintf("• *وقت الوصول:* %s", c.getString(item, "time_of_arrival")))
	}
	if c.hasValue(item, "duration") {
		parts = append(parts, fmt.Sprintf("• *مدة الرحلة:* %s", c.getString(item, "duration")))
	}
	if c.hasValue(item, "usd_price") {
		parts = append(parts, fmt.Sprintf("• *السعر:* %s دولار أمريكي", c.getString(item, "usd_price")))
	}
	if c.hasValue(item, "syp_price") {
		parts = append(parts, fmt.Sprintf("• *السعر:* %s ليرة سورية", c.getString(item, "syp_price")))
	}
	if c.hasValue(item, "airline") {
		parts = append(parts, fmt.Sprintf("• *شركة الطيران:* %s", c.getString(item, "airline")))
	}
	if c.hasValue(item, "notes") {
		parts = append(parts, fmt.Sprintf("• *ملاحظات:* %s", c.getString(item, "notes")))
	}
	return strings.Join(parts, "\n")
}

func (c *ConversationManager) formatOfferDetails(item map[string]interface{}) string {
	parts := []string{fmt.Sprintf("إليك تفاصيل: *%s*", c.getString(item, "name"))}

	if c.hasValue(item, "depart") && c.hasValue(item, "destination") {
		parts = append(parts, fmt.Sprintf("*المسار:* من %s إلى %s", c.getString(item, "depart"), c.getString(item, "destination")))
	}
	if c.hasValue(item, "usd_price") {
		parts = append(parts, fmt.Sprintf("*السعر:* %s دولار أمريكي", c.getString(item, "usd_price")))
	}
	if c.hasValue(item, "syp_price") {
		parts = append(parts, fmt.Sprintf("*السعر:* %s ليرة سورية", c.getString(item, "syp_price")))
	}
	if c.hasValue(item, "details") {
		parts = append(parts, fmt.Sprintf("*التفاصيل:* %s", c.getString(item, "details")))
	}
	if c.hasValue(item, "valid_until") {
		parts = append(parts, fmt.Sprintf("*صالح لغاية:* %s", c.getString(item, "valid_until")))
	}
	if c.hasValue(item, "notes") {
		parts = append(parts, fmt.Sprintf("*ملاحظات:* %s", c.getString(item, "notes")))
	}
	return strings.Join(parts, "\n\n")
}

func (c *ConversationManager) formatServiceDetails(item map[string]interface{}) string {
	parts := []string{fmt.Sprintf("إليك تفاصيل خدمة: *%s*", c.getString(item, "service"))}

	if c.hasValue(item, "usd_price") {
		parts = append(parts, fmt.Sprintf("• *السعر:* %s دولار أمريكي", c.getString(item, "usd_price")))
	}
	if c.hasValue(item, "syp_price") {
		parts = append(parts, fmt.Sprintf("• *السعر:* %s ليرة سورية", c.getString(item, "syp_price")))
	}
	if c.hasValue(item, "details") {
		parts = append(parts, fmt.Sprintf("• *التفاصيل:* %s", c.getString(item, "details")))
	}
	if c.hasValue(item, "notes") {
		parts = append(parts, fmt.Sprintf("• *ملاحظات:* %s", c.getString(item, "notes")))
	}
	return strings.Join(parts, "\n\n")
}

func (c *ConversationManager) formatUmrahDetails(item map[string]interface{}) string {
	parts := []string{fmt.Sprintf("*%s*", c.getString(item, "name_and_type"))}

	if c.hasValue(item, "usd_price") {
		parts = append(parts, fmt.Sprintf("*السعر:* %s دولار أمريكي", c.getString(item, "usd_price")))
	}
	if c.hasValue(item, "syp_price") {
		parts = append(parts, fmt.Sprintf("*السعر:* %s ليرة سورية", c.getString(item, "syp_price")))
	}
	if c.hasValue(item, "duration") {
		parts = append(parts, fmt.Sprintf("*المدة:* %s يوم", c.getString(item, "duration")))
	}
	if c.hasValue(item, "last_date_for_register") {
		parts = append(parts, fmt.Sprintf("*آخر وقت للتسجيل:* %s", c.getString(item, "last_date_for_register")))
	}
	if c.hasValue(item, "company_of_trasnport") {
		parts = append(parts, fmt.Sprintf("*الشركة:* %s", c.getString(item, "company_of_trasnport")))
	}
	if c.hasValue(item, "estimated_time") {
		parts = append(parts, fmt.Sprintf("*مدة الانجاز:* %s", c.getString(item, "estimated_time")))
	}

	hotelTypeMap := map[string]string{
		"1": "فردي", "2": "ثنائي", "3": "ثلاثي", "4": "رباعي",
		"5": "خماسي", "6": "سداسي", "7": "سباعي", "8": "ثماني",
		"9": "تساعي", "10": "عشاري",
	}

	if c.hasValue(item, "type_of_hotel") {
		hotelType := c.getString(item, "type_of_hotel")
		if mappedVal, exists := hotelTypeMap[hotelType]; exists {
			parts = append(parts, fmt.Sprintf("*السكن:* %s", mappedVal))
		} else {
			parts = append(parts, fmt.Sprintf("*السكن:* %s", hotelType))
		}
	}
	if c.hasValue(item, "hotel_category") {
		parts = append(parts, fmt.Sprintf("*تصنيف الفندق:* %s نجوم", c.getString(item, "hotel_category")))
	}
	if c.hasValue(item, "details") {
		parts = append(parts, fmt.Sprintf("*التفاصيل:* %s", c.getString(item, "details")))
	}
	if c.hasValue(item, "notes") {
		parts = append(parts, fmt.Sprintf("*الملاحظات:* %s", c.getString(item, "notes")))
	}
	return strings.Join(parts, "\n")
}

func (c *ConversationManager) formatVisaDetails(item map[string]interface{}) string {
	title := fmt.Sprintf("*%s إلى %s*", c.getString(item, "type"), c.getString(item, "country"))
	parts := []string{title}

	if c.hasValue(item, "usd_price") {
		parts = append(parts, fmt.Sprintf("• *السعر:* %s دولار أمريكي", c.getString(item, "usd_price")))
	}
	if c.hasValue(item, "syp_price") {
		parts = append(parts, fmt.Sprintf("• *السعر:* %s ليرة سورية", c.getString(item, "syp_price")))
	}
	if c.hasValue(item, "estimated_time") {
		parts = append(parts, fmt.Sprintf("• *المدة التقديرية:* %s", c.getString(item, "estimated_time")))
	}
	if c.hasValue(item, "required_papers") {
		parts = append(parts, fmt.Sprintf("• *الأوراق المطلوبة:* %s", c.getString(item, "required_papers")))
	}
	if c.hasValue(item, "valid_until") {
		parts = append(parts, fmt.Sprintf("• *صلاحية الفيزا:* %s", c.getString(item, "valid_until")))
	}
	if c.hasValue(item, "notes") {
		parts = append(parts, fmt.Sprintf("• *ملاحظات:* %s", c.getString(item, "notes")))
	}
	return strings.Join(parts, "\n")
}

func (c *ConversationManager) handleNumericChoice(ctx context.Context, senderID string, messageBody string, session models.UserSession, lang string) (bool, error) {
	sessionContext := session.Context

	stepInter, hasStep := sessionContext["step"]
	if !hasStep || !c.isDigit(messageBody) {
		return false, nil
	}

	step, isString := stepInter.(string)
	if !isString {
		return false, nil
	}

	var data []map[string]interface{}
	dataInter, hasData := sessionContext["data"]
	if hasData {
		dataBytes, _ := json.Marshal(dataInter)
		json.Unmarshal(dataBytes, &data)
	}

	choiceIndex, err := strconv.Atoi(messageBody)
	if err != nil {
		return false, nil
	}
	choiceIndex -= 1

	if choiceIndex < 0 || choiceIndex >= len(data) {
		c.whatsapp.SendMessage(ctx, senderID, "خيار غير صالح. يرجى اختيار رقم من القائمة.")
		return true, nil
	}

	selectedItem := data[choiceIndex]
	newSession := models.NewUserSession()
	for k, v := range session.Context {
		newSession.Context[k] = v
	}

	var responseTextAr string

	switch step {
	case "awaiting_umrah_choice":
		responseTextAr = c.formatUmrahDetails(selectedItem)
	case "awaiting_service_choice":
		responseTextAr = c.formatServiceDetails(selectedItem)
	case "awaiting_offer_choice":
		responseTextAr = c.formatOfferDetails(selectedItem)
	case "awaiting_flight_choice":
		responseTextAr = c.formatFlightDetails(selectedItem)
	case "awaiting_visa_country_choice":
		country := c.getString(selectedItem, "country")
		if country == "" {
			if strVal, ok := selectedItem["value"].(string); ok {
				country = strVal
			}
		}
		allVisas := c.sheets.GetData("visas")
		var countryVisas []map[string]interface{}
		for _, v := range allVisas {
			if c.normalizeArabic(strings.ToLower(c.getString(v, "country"))) == c.normalizeArabic(strings.ToLower(country)) {
				countryVisas = append(countryVisas, v)
			}
		}

		summaryLines := []string{fmt.Sprintf("اختر نوع الفيزا لدولة *%s*:", country), ""}
		for i, visa := range countryVisas {
			validity := ""
			if c.hasValue(visa, "valid_until") {
				validity = fmt.Sprintf("- (صالحة لمدة) %s", c.getString(visa, "valid_until"))
			}
			summaryLines = append(summaryLines, fmt.Sprintf("%d. %s %s %s", i+1, country, c.getStringDefault(visa, "type", "N/A"), validity))
		}
		summaryLines = append(summaryLines, "\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")
		responseTextAr = strings.Join(summaryLines, "\n")

		newSession.Context["step"] = "awaiting_visa_details_choice"
		newSession.Context["data"] = countryVisas
	case "awaiting_visa_type_choice":
		visaType := c.getString(selectedItem, "type")
		if visaType == "" {
			if strVal, ok := selectedItem["value"].(string); ok {
				visaType = strVal
			}
		}
		allVisas := c.sheets.GetData("visas")
		var typeVisas []map[string]interface{}
		for _, v := range allVisas {
			if c.normalizeArabic(strings.ToLower(c.getString(v, "type"))) == c.normalizeArabic(strings.ToLower(visaType)) {
				typeVisas = append(typeVisas, v)
			}
		}

		summaryLines := []string{fmt.Sprintf("اختر الدولة لفيزا (*%s*):", visaType), ""}
		for i, visa := range typeVisas {
			price := ""
			if c.hasValue(visa, "usd_price") {
				price = fmt.Sprintf("- %s$", c.getString(visa, "usd_price"))
			}
			summaryLines = append(summaryLines, fmt.Sprintf("%d. %s %s", i+1, c.getStringDefault(visa, "country", "N/A"), price))
		}
		summaryLines = append(summaryLines, "\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")
		responseTextAr = strings.Join(summaryLines, "\n")

		newSession.Context["step"] = "awaiting_visa_details_choice"
		newSession.Context["data"] = typeVisas
	case "awaiting_visa_details_choice":
		responseTextAr = c.formatVisaDetails(selectedItem)
	default:
		return false, nil
	}

	finalResponseText := responseTextAr
	if lang == "en" {
		finalResponseText = c.translateTextForUser(ctx, responseTextAr)
	}

	stepVal, hasNewStep := newSession.Context["step"]
	if hasNewStep && (stepVal == "awaiting_visa_details_choice" || stepVal == "awaiting_visa_country_choice" || stepVal == "awaiting_visa_type_choice") {
		c.db.UpdateUserSession(ctx, senderID, newSession)
	} else {
		finalSession := models.NewUserSession()
		finalSession.Context["lang"] = lang
		c.db.UpdateUserSession(ctx, senderID, finalSession)
	}

	c.whatsapp.SendMessage(ctx, senderID, finalResponseText)
	c.db.AddMessageToHistory(ctx, senderID, "assistant", finalResponseText)
	return true, nil
}

func (c *ConversationManager) handleRoutineMessage(message string) string {
	messageLower := strings.TrimSpace(strings.ToLower(message))
	if response, exists := c.ROUTINE_RESPONSES[messageLower]; exists {
		return response
	}
	return ""
}

func (c *ConversationManager) initiateHumanHandoff(ctx context.Context, senderID string, lang string, reason string, details string) {
	log := logger.With("user_id", senderID, "reason", reason, "details", details)
	log.Info("Initiating human handoff.")
	
	humanSession := models.NewUserSession()
	humanSession.State = "human"
	c.db.UpdateUserSession(ctx, senderID, humanSession)

	detailsText := ""
	if details != "" {
		detailsText = fmt.Sprintf(" بخصوص '%s'", details)
	}
	handoffPrompt := fmt.Sprintf("أنت مساعد آلي ودود ومتعاون في شركة 'العسل للسياحة والسفر'. مهمتك هي كتابة رسالة قصيرة ولطيفة لإبلاغ العميل بأنه سيتم تحويله الآن إلى موظف بشري. السبب هو '%s'%s. اكتب رسالة طبيعية ومطمئنة باللغة العربية، تشرح فيها أن الموظف سيتابع معه لإكمال طلبه.", reason, detailsText)

	response, err := c.openai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       c.settings.ChatModel,
		Messages:    []openai.ChatCompletionMessage{{Role: "system", Content: handoffPrompt}},
		Temperature: 0.7,
		MaxTokens:   100,
	})

	if err == nil && len(response.Choices) > 0 {
		message := strings.TrimSpace(response.Choices[0].Message.Content)
		finalMessage := message
		if lang == "en" {
			finalMessage = c.translateTextForUser(ctx, message)
		}
		c.whatsapp.SendMessage(ctx, senderID, finalMessage)
		notificationReason := reason
		if details != "" {
			notificationReason = fmt.Sprintf("%s: %s", reason, details)
		}
		c.notifyEmployee(ctx, senderID, notificationReason)
	} else {
		log.Error("Failed to send handoff notification or message", "error", err)
	}
}

func (c *ConversationManager) isCountrySearch(term string, allFlights []map[string]interface{}, isDestination bool) bool {
	termNormalized := c.normalizeArabic(strings.ToLower(term))
	airportColumn := "depart_airport"
	countryColumn := "from_country"
	if isDestination {
		airportColumn = "destination_airport"
		countryColumn = "to_country"
	}

	for _, flight := range allFlights {
		if termNormalized == c.normalizeArabic(strings.ToLower(c.getString(flight, airportColumn))) {
			return false
		}
	}

	for _, flight := range allFlights {
		if termNormalized == c.normalizeArabic(strings.ToLower(c.getString(flight, countryColumn))) {
			return true
		}
	}
	return false
}

func (c *ConversationManager) normalizeArabic(text string) string {
	text = arabicRegex1.ReplaceAllString(text, "ا")
	text = arabicRegex2.ReplaceAllString(text, "ي")
	text = arabicRegex3.ReplaceAllString(text, "ه")
	text = arabicTashkeel.ReplaceAllString(text, "")
	return text
}

func (c *ConversationManager) normalizeNumbers(text string) string {
	return arabicNumeralsReplacer.Replace(text)
}

func (c *ConversationManager) notifyEmployee(ctx context.Context, customerID string, reason string) {
	if c.settings.EmployeeWhatsappNumber == "" {
		logger.Warn("EmployeeWhatsappNumber is not set.")
		return
	}
	notificationMessage := strings.Replace(c.EMPLOYEE_NOTIFICATION_TEMPLATE, "{customer_id}", customerID, 1)
	notificationMessage = strings.Replace(notificationMessage, "{reason}", reason, 1)
	c.whatsapp.SendMessage(ctx, c.settings.EmployeeWhatsappNumber, notificationMessage)
}

func (c *ConversationManager) parseDate(dateStr string) *time.Time {
	formatsToTry := []string{"2006-01-02", "01/02/2006", "02/01/2006"}
	for _, fmtStr := range formatsToTry {
		if parsed, err := time.Parse(fmtStr, dateStr); err == nil {
			return &parsed
		}
	}
	return nil
}

func (c *ConversationManager) sendSummaryList(ctx context.Context, senderID string, session models.UserSession, items []map[string]interface{}, title string, step string, formatter func(map[string]interface{}) string, lang string) {
	if len(items) == 0 {
		noResultsTextAr := fmt.Sprintf("عفواً، لا توجد %s متاحة حالياً.", title)
		finalText := noResultsTextAr
		if lang == "en" {
			finalText = c.translateTextForUser(ctx, noResultsTextAr)
		}
		c.whatsapp.SendMessage(ctx, senderID, finalText)
		
		botSession := models.NewUserSession()
		botSession.Context["lang"] = lang
		c.db.UpdateUserSession(ctx, senderID, botSession)
		return
	}

	summaryLines := []string{fmt.Sprintf("أهلاً بك، هذه هي %s المتوفرة لدينا حالياً:", title), ""}
	for i, item := range items {
		summaryLines = append(summaryLines, fmt.Sprintf("%d. %s", i+1, formatter(item)))
	}
	summaryLines = append(summaryLines, "\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")

	responseTextAr := strings.Join(summaryLines, "\n")
	finalResponseText := responseTextAr
	if lang == "en" {
		finalResponseText = c.translateTextForUser(ctx, responseTextAr)
	}

	session.Context["step"] = step
	session.Context["data"] = items

	c.db.UpdateUserSession(ctx, senderID, session)
	c.whatsapp.SendMessage(ctx, senderID, finalResponseText)
	c.db.AddMessageToHistory(ctx, senderID, "assistant", finalResponseText)
}

func (c *ConversationManager) stripEmojis(text string) string {
	return emojiPattern.ReplaceAllString(text, "")
}

func (c *ConversationManager) translateTextForUser(ctx context.Context, textToTranslate string) string {
	log := logger.With("text_length", len(textToTranslate))
	log.Info("Translating text to English")

	messages := []openai.ChatCompletionMessage{
		{Role: "system", Content: "You are a professional translator. Your task is to translate the following Arabic text to English for a travel agency's WhatsApp bot. The translation must be accurate, professional, and friendly. Preserve the WhatsApp markdown formatting (like *bold text*). Do not add any extra text or commentary, only provide the translation."},
		{Role: "user", Content: textToTranslate},
	}

	response, err := c.openai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       c.settings.ChatModel,
		Messages:    messages,
		Temperature: 0.1,
	})

	if err == nil && len(response.Choices) > 0 {
		translatedText := response.Choices[0].Message.Content
		if translatedText != "" {
			log.Info("Text translated successfully")
			return translatedText
		}
	}
	log.Error("Failed to translate text", "error", err)
	return textToTranslate
}

func (c *ConversationManager) HandleIncomingMessage(ctx context.Context, senderID string, messageBody string) {
	log := logger.With("user_id", senderID)

	if strings.Contains(senderID, "g.us") {
		log.Info("Ignoring group message.")
		return
	}

	if messageBody == "" {
		log.Info("Ignoring message with empty body (likely non-text).")
		return
	}

	cleanedBody := strings.TrimSpace(c.stripEmojis(messageBody))
	cleanedBody = c.normalizeNumbers(cleanedBody)

	if cleanedBody == "" {
		log.Info("Ignoring message containing only emojis or whitespace.")
		return
	}

	session := c.db.GetUserSession(ctx, senderID)
	lang := "ar"
	if session.Context == nil {
		session.Context = make(map[string]interface{})
	}
	session.Context["lang"] = lang
	c.db.UpdateUserSession(ctx, senderID, session)

	if session.State == "human" {
		c.db.AddMessageToHistory(ctx, senderID, "user", cleanedBody)
		return
	}

	c.db.AddMessageToHistory(ctx, senderID, "user", cleanedBody)

	handled, _ := c.handleNumericChoice(ctx, senderID, cleanedBody, session, lang)
	if handled {
		return
	}

	routineResponseAr := c.handleRoutineMessage(cleanedBody)
	if routineResponseAr != "" {
		c.whatsapp.SendMessage(ctx, senderID, routineResponseAr)
		c.db.AddMessageToHistory(ctx, senderID, "assistant", routineResponseAr)
		return
	}

	history := c.db.GetRecentMessages(ctx, senderID, c.settings.OpenAIContextMessages)
	messages := []openai.ChatCompletionMessage{{Role: "system", Content: c.SYSTEM_PROMPT}}
	messages = append(messages, history...)

	tools := []openai.Tool{
		{Type: "function", Function: &openai.FunctionDefinition{Name: "list_services", Description: "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن الخدمات المتوفرة، مثل 'ما هي خدماتكم؟' أو 'شو عندكم خدمات؟'."}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "find_service", Description: "تستخدم للبحث عن خدمة *محددة* عندما يذكر المستخدم تفاصيل عنها. لا تستخدمها للأسئلة العامة عن الخدمات.", Parameters: json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string", "description": "نص البحث الذي يصف الخدمة المطلوبة. مثال: 'سيارة للإيجار' أو 'تجديد جواز السفر'"}}, "required": ["query"]}`)}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "list_offers", Description: "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن العروض، مثل 'ما هي عروضكم؟'."}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "list_umrah_packages", Description: "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن باقات العمرة."}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "get_all_company_info", Description: "للحصول على معلومات ثابتة عن الشركة."}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "list_flights", Description: "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن رحلات الطيران المتوفرة دون تحديد وجهة أو تاريخ."}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "find_flights", Description: "تستخدم للبحث عن رحلات طيران *محددة*. لا تستخدم هذه الأداة إذا كان المستخدم يسأل سؤالاً عاماً عن 'تفاصيل السفر' أو 'إجراءات السفر' لدولة ما، بل استخدمها فقط عندما يكون الطلب واضحاً عن **تذكرة طيران**.", Parameters: json.RawMessage(`{"type": "object", "properties": {"destination": {"type": "string", "description": "وجهة السفر (مدينة أو دولة)"}, "origin": {"type": "string", "description": "نقطة الانطلاق (مدينة أو دولة)"}, "time_query": {"type": "string", "description": "استعلام الوقت كما يعبر عنه المستخدم بالضبط (مثال: 'الأسبوع القادم'، 'بعد غد'، 'رحلات آخر الشهر'، 'يومي')."}}, "required": ["destination"]}`)}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "initiate_visa_discovery", Description: "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً جداً عن الفيزا **دون ذكر اسم أي دولة**، مثل 'ما هي أنواع الفيزا لديكم؟' أو 'ما هي الدول التي توفرون لها فيزا؟'. **لا تستخدمها إذا ذكر المستخدم اسم دولة معينة**.", Parameters: json.RawMessage(`{"type": "object", "properties": {"topic": {"type": "string", "description": "حدد 'countries' إذا سأل عن الدول، أو 'types' إذا سأل عن أنواع الفيزا.", "enum": ["countries", "types"]}}, "required": ["topic"]}`)}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "find_visa_details", Description: "للبحث عن تفاصيل الفيزا لدولة معينة.", Parameters: json.RawMessage(`{"type": "object", "properties": {"country": {"type": "string", "description": "اسم الدولة"}}, "required": ["country"]}`)}},
		{Type: "function", Function: &openai.FunctionDefinition{Name: "initiate_human_handoff", Description: "تستخدم هذه الأداة *فقط* عندما يطلب المستخدم بوضوح التحدث إلى موظف أو عندما يستفسر عن أمور تتطلب تدخلاً بشرياً إلزامياً مثل تثبيت الحجوزات.", Parameters: json.RawMessage(`{"type": "object", "properties": {"reason": {"type": "string", "description": "سبب التحويل. يجب أن يكون واحداً من القيم التالية بناءً على طلب المستخدم.", "enum": ["تثبيت حجز تذكرة", "تثبيت عرض", "تثبيت عمرة", "تثبيت خدمة", "طلب مساعدة مباشرة", "استفسار عن سعر"]}, "details": {"type": "string", "description": "تفاصيل إضافية حول الطلب. إذا كان الطلب هو تثبيت خدمة، يجب أن يحتوي هذا الحقل على اسم الخدمة. مثال: 'سيارة هيونداي توسان' أو 'رحلة إلى دبي'."}}, "required": ["reason"]}`)}},
	}

	response, err := c.openai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    c.settings.ChatModel,
		Messages: messages,
		Tools:    tools,
	})

	if err != nil || len(response.Choices) == 0 {
		log.Error("Error during message processing or empty choices.", "error", err)
		c.initiateHumanHandoff(ctx, senderID, lang, "فشل فني في النظام.", "")
		return
	}

	responseMessage := response.Choices[0].Message
	if len(responseMessage.ToolCalls) > 0 {
		messages = append(messages, responseMessage)
		c.handleToolCall(ctx, senderID, responseMessage.ToolCalls[0], session, messages, lang)
	} else {
		finalResponseText := responseMessage.Content
		c.whatsapp.SendMessage(ctx, senderID, finalResponseText)
		c.db.AddMessageToHistory(ctx, senderID, "assistant", finalResponseText)
	}
}

func (c *ConversationManager) handleToolCall(ctx context.Context, senderID string, toolCall openai.ToolCall, session models.UserSession, messages []openai.ChatCompletionMessage, lang string) {
	functionName := toolCall.Function.Name
	var args map[string]interface{}
	json.Unmarshal([]byte(toolCall.Function.Arguments), &args)

	log := logger.With("user_id", senderID, "tool", functionName, "args", args)
	log.Info("Handling tool call")

	switch functionName {
	case "initiate_human_handoff":
		reason := c.getStringDefault(args, "reason", "طلب المستخدم التحدث إلى موظف")
		details := c.getString(args, "details")
		c.initiateHumanHandoff(ctx, senderID, lang, reason, details)

	case "list_services":
		log.Info("Listing all available services")
		allServices := c.sheets.GetData("services")
		var availableServices []map[string]interface{}
		for _, s := range allServices {
			if strings.ToLower(c.getString(s, "is_it_available")) == "نعم" {
				availableServices = append(availableServices, s)
			}
		}
		c.sendSummaryList(ctx, senderID, session, availableServices, "الخدمات المتوفرة", "awaiting_service_choice", func(item map[string]interface{}) string { return c.getStringDefault(item, "service", "N/A") }, lang)

	case "find_service":
		query := c.getString(args, "query")
		log.Info("Finding service with AI-powered search", "query", query)

		allServices := c.sheets.GetData("services")
		var availableServices []map[string]interface{}
		for _, s := range allServices {
			if strings.ToLower(c.getString(s, "is_it_available")) == "نعم" {
				availableServices = append(availableServices, s)
			}
		}

		if len(availableServices) == 0 {
			noResultsTextAr := "عفواً، لا تتوفر لدينا أي خدمات حالياً."
			c.whatsapp.SendMessage(ctx, senderID, noResultsTextAr)
			return
		}

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.Encode(availableServices)
		servicesJSON := buf.String()

		filteringPrompt := fmt.Sprintf("أنت خبير في مطابقة خدمات السفر. مهمتك هي تحليل طلب المستخدم وإيجاد أفضل خدمة مطابقة له من قائمة الخدمات المتوفرة.\n\n- طلب المستخدم هو: '%s'\n- قائمة الخدمات (JSON): %s\n\nالرجاء إعادة قائمة JSON تحتوي *فقط* على الخدمة (أو الخدمات) التي تلبي طلب المستخدم بشكل مباشر. إذا لم تكن هناك خدمة مطابقة تماماً، أعد قائمة فارغة [].", query, servicesJSON)

		response, err := c.openai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model: c.settings.ChatModel,
			Messages: []openai.ChatCompletionMessage{
				{Role: "system", Content: filteringPrompt},
			},
			ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
			Temperature:    0.0,
		})

		var matchingServices []map[string]interface{}
		if err == nil && len(response.Choices) > 0 {
			responseContent := response.Choices[0].Message.Content
			jsonMatch := regexp.MustCompile(`(?s)\[.*\]`).FindString(responseContent)
			if jsonMatch != "" {
				json.Unmarshal([]byte(jsonMatch), &matchingServices)
			} else {
				logger.Warn("AI did not return a valid JSON list for service filtering.", "raw_response", responseContent)
			}
		}

		if len(matchingServices) == 0 {
			noResultsTextAr := "عفواً، لا تتوفر لدينا هذه الخدمة حالياً."
			c.whatsapp.SendMessage(ctx, senderID, noResultsTextAr)
			return
		}

		if len(matchingServices) == 1 {
			responseTextAr := c.formatServiceDetails(matchingServices[0])
			c.whatsapp.SendMessage(ctx, senderID, responseTextAr)
			c.db.AddMessageToHistory(ctx, senderID, "assistant", responseTextAr)
		} else {
			c.sendSummaryList(ctx, senderID, session, matchingServices, "الخدمات المطابقة لبحثك", "awaiting_service_choice", func(item map[string]interface{}) string { return c.getStringDefault(item, "service", "N/A") }, lang)
		}

	case "list_offers":
		offers := c.sheets.GetData("offers")
		c.sendSummaryList(ctx, senderID, session, offers, "العروض السياحية", "awaiting_offer_choice", func(item map[string]interface{}) string { return c.getStringDefault(item, "name", "N/A") }, lang)

	case "list_umrah_packages":
		packages := c.sheets.GetData("umrah")
		c.sendSummaryList(ctx, senderID, session, packages, "باقات العمرة", "awaiting_umrah_choice", func(item map[string]interface{}) string { return c.getStringDefault(item, "name_and_type", "N/A") }, lang)

	case "list_flights":
		flights := c.sheets.GetData("flights")
		c.sendSummaryList(ctx, senderID, session, flights, "رحلات الطيران", "awaiting_flight_choice", c.flightFormatter, lang)

	case "get_all_company_info":
		allInfo := c.sheets.GetData("informations")

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.Encode(allInfo)
		infoContent := buf.String()

		messages = append(messages, openai.ChatCompletionMessage{Role: "tool", ToolCallID: toolCall.ID, Name: functionName, Content: infoContent})

		response, err := c.openai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    c.settings.ChatModel,
			Messages: messages,
		})

		if err == nil && len(response.Choices) > 0 {
			responseText := response.Choices[0].Message.Content
			c.whatsapp.SendMessage(ctx, senderID, responseText)
			c.db.AddMessageToHistory(ctx, senderID, "assistant", responseText)
		}

	case "find_flights":
		destination := c.getString(args, "destination")
		origin := c.getString(args, "origin")
		timeQuery := c.getString(args, "time_query")

		destinationNormalized := c.normalizeArabic(strings.ToLower(destination))
		originNormalized := c.normalizeArabic(strings.ToLower(origin))
		allFlights := c.sheets.GetData("flights")

		filteredFlights := allFlights
		if destinationNormalized != "" {
			var temp []map[string]interface{}
			isCountry := c.isCountrySearch(destination, allFlights, true)
			for _, f := range filteredFlights {
				col := "destination_airport"
				if isCountry {
					col = "to_country"
				}
				if strings.Contains(c.normalizeArabic(strings.ToLower(c.getString(f, col))), destinationNormalized) {
					temp = append(temp, f)
				}
			}
			filteredFlights = temp
		}

		if originNormalized != "" {
			var temp []map[string]interface{}
			isCountry := c.isCountrySearch(origin, allFlights, false)
			for _, f := range filteredFlights {
				col := "depart_airport"
				if isCountry {
					col = "from_country"
				}
				if strings.Contains(c.normalizeArabic(strings.ToLower(c.getString(f, col))), originNormalized) {
					temp = append(temp, f)
				}
			}
			filteredFlights = temp
		}

		matchingFlights := filteredFlights
		if timeQuery != "" && len(filteredFlights) > 0 {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.Encode(filteredFlights)
			flightsJSON := buf.String()
			todayDate := time.Now().Format("2006-01-02")

			filteringPrompt := fmt.Sprintf("أنت خبير في تحليل البيانات. أمامك قائمة رحلات طيران بصيغة JSON. مهمتك هي ترشيح هذه القائمة بناءً على طلب المستخدم الزمني.\n\n- تاريخ اليوم هو: %s\n- طلب المستخدم الزمني هو: '%s'\n- بيانات الرحلات: %s\n\nالرجاء إعادة قائمة JSON تحتوي فقط على الرحلات التي تتطابق بدقة مع طلب المستخدم. إذا لم توجد أي رحلات مطابقة، أعد قائمة فارغة [].", todayDate, timeQuery, flightsJSON)

			response, err := c.openai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
				Model: c.settings.ChatModel,
				Messages: []openai.ChatCompletionMessage{
					{Role: "system", Content: filteringPrompt},
				},
				ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
				Temperature:    0.0,
			})

			matchingFlights = []map[string]interface{}{}
			if err == nil && len(response.Choices) > 0 {
				responseContent := response.Choices[0].Message.Content
				jsonMatch := regexp.MustCompile(`(?s)\[.*\]`).FindString(responseContent)
				if jsonMatch != "" {
					json.Unmarshal([]byte(jsonMatch), &matchingFlights)
				}
			}
		}

		title := fmt.Sprintf("الرحلات القادمة إلى %s", destination)
		if origin != "" {
			title = fmt.Sprintf("الرحلات القادمة من %s إلى %s", origin, destination)
		}
		c.sendSummaryList(ctx, senderID, session, matchingFlights, title, "awaiting_flight_choice", c.flightFormatter, lang)

	case "initiate_visa_discovery":
		topic := c.getString(args, "topic")
		allVisas := c.sheets.GetData("visas")
		if topic == "countries" {
			countryMap := make(map[string]bool)
			for _, v := range allVisas {
				if country := c.getString(v, "country"); country != "" {
					countryMap[country] = true
				}
			}
			var countries []string
			for country := range countryMap {
				countries = append(countries, country)
			}
			sort.Strings(countries)

			var countriesItems []map[string]interface{}
			for _, c := range countries {
				countriesItems = append(countriesItems, map[string]interface{}{"value": c})
			}
			c.sendSummaryList(ctx, senderID, session, countriesItems, "الدول التي نوفر لها فيزا", "awaiting_visa_country_choice", func(item map[string]interface{}) string { return c.getString(item, "value") }, lang)
		} else if topic == "types" {
			typeMap := make(map[string]bool)
			for _, v := range allVisas {
				if vType := c.getString(v, "type"); vType != "" {
					typeMap[vType] = true
				}
			}
			var types []string
			for t := range typeMap {
				types = append(types, t)
			}
			sort.Strings(types)

			var typesItems []map[string]interface{}
			for _, t := range types {
				typesItems = append(typesItems, map[string]interface{}{"value": t})
			}
			c.sendSummaryList(ctx, senderID, session, typesItems, "أنواع الفيزا المتوفرة", "awaiting_visa_type_choice", func(item map[string]interface{}) string { return c.getString(item, "value") }, lang)
		}

	case "find_visa_details":
		countryNormalized := c.normalizeArabic(strings.ToLower(c.getString(args, "country")))
		allVisas := c.sheets.GetData("visas")
		var visas []map[string]interface{}
		for _, v := range allVisas {
			if c.normalizeArabic(strings.ToLower(c.getString(v, "country"))) == countryNormalized {
				visas = append(visas, v)
			}
		}

		if len(visas) == 0 {
			noVisaTextAr := fmt.Sprintf("عفواً، لا توجد معلومات عن فيزا لدولة *%s*.", c.getString(args, "country"))
			c.whatsapp.SendMessage(ctx, senderID, noVisaTextAr)
			return
		}

		if len(visas) == 1 {
			responseTextAr := c.formatVisaDetails(visas[0])
			c.whatsapp.SendMessage(ctx, senderID, responseTextAr)
			c.db.AddMessageToHistory(ctx, senderID, "assistant", responseTextAr)
		} else {
			summaryLines := []string{fmt.Sprintf("اختر نوع الفيزا لدولة *%s*:", c.getString(args, "country")), ""}
			for i, visa := range visas {
				validity := ""
				if c.hasValue(visa, "valid_until") {
					validity = fmt.Sprintf("- (صالحة لمدة) %s", c.getString(visa, "valid_until"))
				}
				summaryLines = append(summaryLines, fmt.Sprintf("%d. %s %s", i+1, c.getStringDefault(visa, "type", "N/A"), validity))
			}
			summaryLines = append(summaryLines, "\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")
			responseTextAr := strings.Join(summaryLines, "\n")

			finalResponseText := responseTextAr
			if lang == "en" {
				finalResponseText = c.translateTextForUser(ctx, responseTextAr)
			}

			session.Context["step"] = "awaiting_visa_details_choice"
			session.Context["data"] = visas
			c.db.UpdateUserSession(ctx, senderID, session)
			c.whatsapp.SendMessage(ctx, senderID, finalResponseText)
			c.db.AddMessageToHistory(ctx, senderID, "assistant", finalResponseText)
		}
	}
}

func (c *ConversationManager) PauseBotForUser(ctx context.Context, userNumber string) {
	humanSession := models.NewUserSession()
	humanSession.State = "human"
	c.db.UpdateUserSession(ctx, userNumber, humanSession)
	
	logger.Info(fmt.Sprintf("Bot paused for user %s by in-chat command.", userNumber))
	confirmationMessage := "تم إيقاف المساعد الآلي. يمكنك الآن التحدث مباشرة مع الموظف"
	c.whatsapp.SendMessage(ctx, userNumber, confirmationMessage)
}

func (c *ConversationManager) ResumeBotForUser(ctx context.Context, userNumber string) {
	botSession := models.NewUserSession()
	c.db.UpdateUserSession(ctx, userNumber, botSession)
	
	logger.Info(fmt.Sprintf("Bot resumed for user %s by in-chat command.", userNumber))
	lastMessage := c.db.GetLastUserMessageContent(ctx, userNumber)
	if lastMessage == "" {
		lastMessage = "ar"
	}
	lang := c.detectLanguage(lastMessage)
	resumeMessage := "المساعد الآلي عاد لخدمتك."
	finalMessage := resumeMessage
	if lang == "en" {
		finalMessage = c.translateTextForUser(ctx, resumeMessage)
	}
	c.whatsapp.SendMessage(ctx, userNumber, finalMessage)
}

func (c *ConversationManager) getString(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok && val != nil {
		if f, isFloat := val.(float64); isFloat {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return fmt.Sprintf("%v", val)
	}
	return ""
}

func (c *ConversationManager) getStringDefault(m map[string]interface{}, key string, def string) string {
	val := c.getString(m, key)
	if val == "" {
		return def
	}
	return val
}

func (c *ConversationManager) hasValue(m map[string]interface{}, key string) bool {
	val, ok := m[key]
	if !ok || val == nil {
		return false
	}
	if strVal, isStr := val.(string); isStr {
		return strVal != ""
	}
	return true
}

func (c *ConversationManager) isDigit(s string) bool {
	for _, char := range s {
		if char < '0' || char > '9' {
			return false
		}
	}
	return len(s) > 0
}
