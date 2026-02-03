import datetime
import json
import re
from typing import Any, Dict, List, Optional

import structlog
from langdetect import LangDetectException, detect

from app.config import Settings
from app.database import DatabaseService
from app.models import UserSession
from app.services import GoogleSheetService, OpenAIService, WhatsAppBridgeService

logger = structlog.get_logger(__name__)


class ConversationManager:
    def __init__(self, db_service: DatabaseService, whatsapp_service: WhatsAppBridgeService, sheet_service: GoogleSheetService, openai_service: OpenAIService, settings: Settings):
        self.db = db_service
        self.whatsapp = whatsapp_service
        self.sheets = sheet_service
        self.openai = openai_service
        self.settings = settings
        self.SYSTEM_PROMPT = self.settings.SYSTEM_PROMPT
        self.EMPLOYEE_NOTIFICATION_TEMPLATE = "*تنبيه: مطلوب تدخل بشري*\n\nالعميل `{customer_id}` بحاجة إلى مساعدة.\n\n*السبب:* {reason}\n\nيرجى فتح واتساب والتواصل معه."
        self.ROUTINE_RESPONSES = {
            ("شكرا", "مشكور", "يسلمو"): "على الرحب والسعة!",
            ("مرحبا", "هلا", "السلام عليكم"): "أهلاً بك. كيف يمكنني خدمتك؟",
            ("تمام", "اوك", "ك"): "بالخدمة.",
        }

    def _detect_language(self, text: str) -> str:
        try:
            return 'ar' if detect(text) == 'ar' else 'en'
        except LangDetectException:
            return 'en'

    def _flight_formatter(self, item: Dict) -> str:
        origin = item.get('depart_airport', 'N/A')
        destination = item.get('destination_airport', 'N/A')
        date = item.get('depart_date', 'N/A')
        flight_info = f"رحلة من {origin} إلى {destination} | بتاريخ {date}"
        price = item.get('usd_price')
        if price:
            flight_info += f" | السعر: {price}$"
        return flight_info

    def _format_flight_details(self, item: Dict) -> str:
        title = f"*{item.get('type', 'رحلة')} إلى {item.get('destination_airport')}*"
        parts = [title]
        if item.get('depart_airport') and item.get('destination_airport'):
            parts.append(f"• *المسار:* من {item['depart_airport']} إلى {item['destination_airport']}")
        if item.get('depart_date'):
            parts.append(f"• *تاريخ الإقلاع:* {item['depart_date']}")
        if item.get('return_date'):
            parts.append(f"• *تاريخ العودة:* {item['return_date']}")
        if item.get('time_of_depart'):
            parts.append(f"• *وقت الإقلاع:* {item['time_of_depart']}")
        if item.get('time_of_arrival'):
            parts.append(f"• *وقت الوصول:* {item['time_of_arrival']}")
        if item.get('duration'):
            parts.append(f"• *مدة الرحلة:* {item['duration']}")
        if item.get('usd_price'):
            parts.append(f"• *السعر:* {item['usd_price']} دولار أمريكي")
        if item.get('syp_price'):
            parts.append(f"• *السعر:* {item['syp_price']} ليرة سورية")
        if item.get('airline'):
            parts.append(f"• *شركة الطيران:* {item['airline']}")
        if item.get('notes'):
            parts.append(f"• *ملاحظات:* {item['notes']}")
        return "\n".join(parts)

    def _format_offer_details(self, item: Dict) -> str:
        parts = [f"إليك تفاصيل: *{item.get('name')}*"]
        if item.get('depart') and item.get('destination'):
            parts.append(f"*المسار:* من {item['depart']} إلى {item['destination']}")
        if item.get('usd_price'):
            parts.append(f"*السعر:* {item['usd_price']} دولار أمريكي")
        if item.get('syp_price'):
            parts.append(f"*السعر:* {item['syp_price']} ليرة سورية")
        if item.get('details'):
            parts.append(f"*التفاصيل:* {item['details']}")
        if item.get('valid_until'):
            parts.append(f"*صالح لغاية:* {item['valid_until']}")
        if item.get('notes'):
            parts.append(f"*ملاحظات:* {item['notes']}")
        return "\n\n".join(parts)

    def _format_service_details(self, item: Dict) -> str:
        parts = [f"إليك تفاصيل خدمة: *{item.get('service')}*"]
        if item.get('usd_price'):
            parts.append(f"• *السعر:* {item['usd_price']} دولار أمريكي")
        if item.get('syp_price'):
            parts.append(f"• *السعر:* {item['syp_price']} ليرة سورية")
        if item.get('details'):
            parts.append(f"• *التفاصيل:* {item['details']}")
        if item.get('notes'):
            parts.append(f"• *ملاحظات:* {item['notes']}")
        return "\n\n".join(parts)

    def _format_umrah_details(self, item: Dict) -> str:
        parts = [f"*{item.get('name_and_type')}*"]
        if item.get('usd_price'):
            parts.append(f"*السعر:* {item['usd_price']} دولار أمريكي")
        if item.get('syp_price'):
            parts.append(f"*السعر:* {item['syp_price']} ليرة سورية")
        if item.get('duration'):
            parts.append(f"*المدة:* {item['duration']} يوم")
        if item.get('last_date_for_register'):
            parts.append(f"*آخر وقت للتسجيل:* {item['last_date_for_register']}")
        if item.get('company_of_trasnport'):
            parts.append(f"*الشركة:* {item['company_of_trasnport']}")
        if item.get('estimated_time'):
            parts.append(f"*مدة الانجاز:* {item['estimated_time']}")
        hotel_type_map = {
            "1": "فردي", "2": "ثنائي", "3": "ثلاثي", "4": "رباعي",
            "5": "خماسي", "6": "سداسي", "7": "سباعي", "8": "ثماني",
            "9": "تساعي", "10": "عشاري"
        }
        if item.get('type_of_hotel'):
            parts.append(f"*السكن:* {hotel_type_map.get(str(item['type_of_hotel']), item['type_of_hotel'])}")
        if item.get('hotel_category'):
            parts.append(f"*تصنيف الفندق:* {item['hotel_category']} نجوم")
        if item.get('details'):
            parts.append(f"*التفاصيل:* {item['details']}")
        if item.get('notes'):
            parts.append(f"*الملاحظات:* {item['notes']}")
        return "\n".join(parts)

    def _format_visa_details(self, item: Dict) -> str:
        title = f"*{item.get('type')} إلى {item.get('country')}*"
        parts = [title]
        if item.get('usd_price'):
            parts.append(f"• *السعر:* {item['usd_price']} دولار أمريكي")
        if item.get('syp_price'):
            parts.append(f"• *السعر:* {item['syp_price']} ليرة سورية")
        if item.get('estimated_time'):
            parts.append(f"• *المدة التقديرية:* {item['estimated_time']}")
        if item.get('required_papers'):
            parts.append(f"• *الأوراق المطلوبة:* {item['required_papers']}")
        if item.get('valid_until'):
            parts.append(f"• *صلاحية الفيزا:* {item['valid_until']}")
        if item.get('notes'):
            parts.append(f"• *ملاحظات:* {item['notes']}")
        return "\n".join(parts)

    async def _handle_numeric_choice(self, sender_id: str, message_body: str, session: UserSession, lang: str) -> bool:
        context = session.context
        if not (context and 'step' in context and message_body.isdigit()):
            return False

        step = context.get('step')
        data = context.get('data', [])

        try:
            choice_index = int(message_body) - 1
            if not (0 <= choice_index < len(data)):
                await self.whatsapp.send_message(sender_id, "خيار غير صالح. يرجى اختيار رقم من القائمة.")
                return True
        except (ValueError, IndexError):
            return False

        selected_item = data[choice_index]
        new_session = UserSession(state='bot', context=session.context)
        response_text_ar = ""

        if step == 'awaiting_umrah_choice':
            response_text_ar = self._format_umrah_details(selected_item)
        elif step == 'awaiting_service_choice':
            response_text_ar = self._format_service_details(selected_item)
        elif step == 'awaiting_offer_choice':
            response_text_ar = self._format_offer_details(selected_item)
        elif step == 'awaiting_flight_choice':
            response_text_ar = self._format_flight_details(selected_item)
        elif step == 'awaiting_visa_country_choice':
            country = selected_item
            all_visas = self.sheets.get_data('visas')
            country_visas = [v for v in all_visas if self._normalize_arabic(str(v.get('country', '')).lower()) == self._normalize_arabic(country.lower())]

            summary_lines = [f"اختر نوع الفيزا لدولة *{country}*:", ""]
            for i, visa in enumerate(country_visas):
                validity = f"- (صالحة لمدة) {visa['valid_until']}" if visa.get('valid_until') else ""
                summary_lines.append(f"{i + 1}. {country} {visa.get('type', 'N/A')} {validity}")
            summary_lines.append("\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")
            response_text_ar = "\n".join(summary_lines)

            new_session.context['step'] = 'awaiting_visa_details_choice'
            new_session.context['data'] = country_visas
        elif step == 'awaiting_visa_type_choice':
            visa_type = selected_item
            all_visas = self.sheets.get_data('visas')
            type_visas = [v for v in all_visas if self._normalize_arabic(str(v.get('type', '')).lower()) == self._normalize_arabic(visa_type.lower())]

            summary_lines = [f"اختر الدولة لفيزا (*{visa_type}*):", ""]
            for i, visa in enumerate(type_visas):
                price = f"- {visa['usd_price']}$" if visa.get('usd_price') else ""
                summary_lines.append(f"{i + 1}. {visa.get('country', 'N/A')} {price}")
            summary_lines.append("\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")
            response_text_ar = "\n".join(summary_lines)

            new_session.context['step'] = 'awaiting_visa_details_choice'
            new_session.context['data'] = type_visas
        elif step == 'awaiting_visa_details_choice':
            response_text_ar = self._format_visa_details(selected_item)
        else:
            return False

        final_response_text = response_text_ar
        if lang == 'en':
            final_response_text = await self._translate_text_for_user(response_text_ar)

        if 'step' in new_session.context and new_session.context['step'] in ['awaiting_visa_details_choice', 'awaiting_visa_country_choice', 'awaiting_visa_type_choice']:
            await self.db.update_user_session(sender_id, new_session)
        else:
            await self.db.update_user_session(sender_id, UserSession(state='bot', context={'lang': lang}))

        await self.whatsapp.send_message(sender_id, final_response_text)
        await self.db.add_message_to_history(sender_id, 'assistant', final_response_text)
        return True

    def _handle_routine_message(self, message: str) -> Optional[str]:
        message_lower = message.lower().strip()
        for keywords, response in self.ROUTINE_RESPONSES.items():
            if any(keyword == message_lower for keyword in keywords):
                return response
        return None

    async def _initiate_human_handoff(self, sender_id: str, lang: str, reason: str, details: Optional[str] = None):
        log = logger.bind(user_id=sender_id)
        log.info("Initiating human handoff.", reason=reason, details=details)
        await self.db.update_user_session(sender_id, UserSession(state='human', context={}))

        try:
            details_text = f" بخصوص '{details}'" if details else ""
            handoff_prompt = (
                f"أنت مساعد آلي ودود ومتعاون في شركة 'العسل للسياحة والسفر'. "
                f"مهمتك هي كتابة رسالة قصيرة ولطيفة لإبلاغ العميل بأنه سيتم تحويله الآن إلى موظف بشري. "
                f"السبب هو '{reason}'{details_text}. "
                f"اكتب رسالة طبيعية ومطمئنة باللغة العربية، تشرح فيها أن الموظف سيتابع معه لإكمال طلبه."
            )

            response = await self.openai.client.chat.completions.create(
                model=self.settings.CHAT_MODEL,
                messages=[{"role": "system", "content": handoff_prompt}],
                temperature=0.7,
                max_tokens=100
            )
            message = response.choices[0].message.content.strip()

            final_message = message
            if lang == 'en':
                final_message = await self._translate_text_for_user(message)
            await self.whatsapp.send_message(sender_id, final_message)
            notification_reason = f"{reason}: {details}" if details else reason
            await self._notify_employee(sender_id, notification_reason)
        except Exception as e:
            log.error("Failed to send handoff notification or message", error=str(e))

    def _is_country_search(self, term: str, all_flights: List[Dict], is_destination: bool) -> bool:
        term_normalized = self._normalize_arabic(term.lower())
        airport_column = 'destination_airport' if is_destination else 'depart_airport'
        country_column = 'to_country' if is_destination else 'from_country'

        for flight in all_flights:
            if term_normalized == self._normalize_arabic(str(flight.get(airport_column, '')).lower()):
                return False

        for flight in all_flights:
            if term_normalized == self._normalize_arabic(str(flight.get(country_column, '')).lower()):
                return True
        return False

    def _normalize_arabic(self, text: str) -> str:
        text = re.sub("[إأآا]", "ا", text)
        text = re.sub("ى", "ي", text)
        text = re.sub("ة", "ه", text)
        text = re.sub(r'[\u064B-\u0652]', '', text)
        return text

    async def _notify_employee(self, customer_id: str, reason: str):
        if not self.settings.EMPLOYEE_WHATSAPP_NUMBER:
            logger.warning("EMPLOYEE_WHATSAPP_NUMBER is not set.")
            return
        notification_message = self.EMPLOYEE_NOTIFICATION_TEMPLATE.format(customer_id=customer_id, reason=reason)
        await self.whatsapp.send_message(self.settings.EMPLOYEE_WHATSAPP_NUMBER, notification_message)

    def _parse_date(self, date_str: str) -> Optional[datetime.date]:
        formats_to_try = ['%Y-%m-%d', '%m/%d/%Y', '%d/%m/%Y']
        for fmt in formats_to_try:
            try:
                return datetime.datetime.strptime(str(date_str), fmt).date()
            except (ValueError, TypeError):
                continue
        return None

    async def _send_summary_list(self, sender_id: str, session: UserSession, items: List[Any], title: str, step: str, formatter, lang: str):
        if not items:
            no_results_text_ar = f"عفواً، لا توجد {title} متاحة حالياً."
            final_text = no_results_text_ar
            if lang == 'en':
                final_text = await self._translate_text_for_user(no_results_text_ar)
            await self.whatsapp.send_message(sender_id, final_text)
            await self.db.update_user_session(sender_id, UserSession(state='bot', context={'lang': lang}))
            return

        summary_lines = [f"أهلاً بك، هذه هي {title} المتوفرة لدينا حالياً:", ""]
        summary_lines.extend([f"{i + 1}. {formatter(item)}" for i, item in enumerate(items)])
        summary_lines.append("\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")

        response_text_ar = "\n".join(summary_lines)

        final_response_text = response_text_ar
        if lang == 'en':
            final_response_text = await self._translate_text_for_user(response_text_ar)

        session.context['step'] = step
        session.context['data'] = items

        await self.db.update_user_session(sender_id, session)
        await self.whatsapp.send_message(sender_id, final_response_text)
        await self.db.add_message_to_history(sender_id, 'assistant', final_response_text)

    def _strip_emojis(self, text: str) -> str:
        emoji_pattern = re.compile(
            "["
            "\U0001F600-\U0001F64F"
            "\U0001F300-\U0001F5FF"
            "\U0001F680-\U0001F6FF"
            "\U0001F1E0-\U0001F1FF"
            "\U00002702-\U000027B0"
            "\U000024C2-\U0001F251"
            "]+",
            flags=re.UNICODE,
        )
        return emoji_pattern.sub(r'', text)

    async def _translate_text_for_user(self, text_to_translate: str) -> str:
        log = logger.bind(text_length=len(text_to_translate))
        log.info("Translating text to English")
        try:
            messages = [
                {"role": "system", "content": "You are a professional translator. Your task is to translate the following Arabic text to English for a travel agency's WhatsApp bot. The translation must be accurate, professional, and friendly. Preserve the WhatsApp markdown formatting (like *bold text*). Do not add any extra text or commentary, only provide the translation."},
                {"role": "user", "content": text_to_translate}
            ]

            response = await self.openai.client.chat.completions.create(
                model=self.settings.CHAT_MODEL,
                messages=messages,
                temperature=0.1
            )
            translated_text = response.choices[0].message.content
            log.info("Text translated successfully")
            return translated_text if translated_text else text_to_translate
        except Exception as e:
            log.error("Failed to translate text", error=str(e))
            return text_to_translate

    async def handle_incoming_message(self, sender_id: str, message_body: Optional[str]):
        log = logger.bind(user_id=sender_id)

        if 'g.us' in sender_id:
            log.info("Ignoring group message.")
            return

        if not message_body:
            log.info("Ignoring message with empty body (likely non-text).")
            return

        cleaned_body = self._strip_emojis(message_body).strip()
        if not cleaned_body:
            log.info("Ignoring message containing only emojis or whitespace.")
            return

        session = await self.db.get_user_session(sender_id)
        lang = 'ar'
        session.context['lang'] = lang
        await self.db.update_user_session(sender_id, session)

        if session.state == 'human':
            await self.db.add_message_to_history(sender_id, 'user', cleaned_body)
            return

        await self.db.add_message_to_history(sender_id, 'user', cleaned_body)

        if await self._handle_numeric_choice(sender_id, cleaned_body, session, lang):
            return

        routine_response_ar = self._handle_routine_message(cleaned_body)
        if routine_response_ar:
            await self.whatsapp.send_message(sender_id, routine_response_ar)
            await self.db.add_message_to_history(sender_id, 'assistant', routine_response_ar)
            return

        history = await self.db.get_recent_messages(sender_id, self.settings.OPENAI_CONTEXT_MESSAGES)
        messages = [{"role": "system", "content": self.SYSTEM_PROMPT}] + history

        tools = [
            {"type": "function", "function": {"name": "list_services", "description": "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن الخدمات المتوفرة، مثل 'ما هي خدماتكم؟' أو 'شو عندكم خدمات؟'."}},
            {"type": "function", "function": {"name": "find_service", "description": "تستخدم للبحث عن خدمة *محددة* عندما يذكر المستخدم تفاصيل عنها. لا تستخدمها للأسئلة العامة عن الخدمات.", "parameters": {"type": "object", "properties": {"query": {"type": "string", "description": "نص البحث الذي يصف الخدمة المطلوبة. مثال: 'سيارة للإيجار' أو 'تجديد جواز السفر'"}}, "required": ["query"]}}},
            {"type": "function", "function": {"name": "list_offers", "description": "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن العروض، مثل 'ما هي عروضكم؟'."}},
            {"type": "function", "function": {"name": "list_umrah_packages", "description": "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن باقات العمرة."}},
            {"type": "function", "function": {"name": "get_all_company_info", "description": "للحصول على معلومات ثابتة عن الشركة."}},
            {"type": "function", "function": {"name": "list_flights", "description": "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً عن رحلات الطيران المتوفرة دون تحديد وجهة أو تاريخ."}},
            {"type": "function", "function": {"name": "find_flights", "description": "تستخدم للبحث عن رحلات طيران *محددة*. لا تستخدم هذه الأداة إذا كان المستخدم يسأل سؤالاً عاماً عن 'تفاصيل السفر' أو 'إجراءات السفر' لدولة ما، بل استخدمها فقط عندما يكون الطلب واضحاً عن **تذكرة طيران**.", "parameters": {"type": "object", "properties": {"destination": {"type": "string", "description": "وجهة السفر (مدينة أو دولة)"}, "origin": {"type": "string", "description": "نقطة الانطلاق (مدينة أو دولة)"}, "time_query": {"type": "string", "description": "استعلام الوقت كما يعبر عنه المستخدم بالضبط (مثال: 'الأسبوع القادم'، 'بعد غد'، 'رحلات آخر الشهر'، 'يومي')."}}, "required": ["destination"]}}},
            {"type": "function", "function": {"name": "initiate_visa_discovery", "description": "تستخدم *فقط* عندما يسأل المستخدم سؤالاً عاماً جداً عن الفيزا **دون ذكر اسم أي دولة**، مثل 'ما هي أنواع الفيزا لديكم؟' أو 'ما هي الدول التي توفرون لها فيزا؟'. **لا تستخدمها إذا ذكر المستخدم اسم دولة معينة**.", "parameters": {"type": "object", "properties": {"topic": {"type": "string", "description": "حدد 'countries' إذا سأل عن الدول، أو 'types' إذا سأل عن أنواع الفيزا.", "enum": ["countries", "types"]}}, "required": ["topic"]}}},
            {"type": "function", "function": {"name": "find_visa_details", "description": "للبحث عن تفاصيل الفيزا لدولة معينة.", "parameters": {"type": "object", "properties": {"country": {"type": "string", "description": "اسم الدولة"}}, "required": ["country"]}}},
            {"type": "function", "function": {"name": "initiate_human_handoff", "description": "تستخدم هذه الأداة *فقط* عندما يطلب المستخدم بوضوح التحدث إلى موظف أو عندما يستفسر عن أمور تتطلب تدخلاً بشرياً إلزامياً مثل تثبيت الحجوزات.", "parameters": {"type": "object", "properties": {"reason": {"type": "string", "description": "سبب التحويل. يجب أن يكون واحداً من القيم التالية بناءً على طلب المستخدم.", "enum": ["تثبيت حجز تذكرة", "تثبيت عرض", "تثبيت عمرة", "تثبيت خدمة", "طلب مساعدة مباشرة", "استفسار عن سعر"]}, "details": {"type": "string", "description": "تفاصيل إضافية حول الطلب. إذا كان الطلب هو تثبيت خدمة، يجب أن يحتوي هذا الحقل على اسم الخدمة. مثال: 'سيارة هيونداي توسان' أو 'رحلة إلى دبي'."}}, "required": ["reason"]}}}
        ]

        try:
            response = await self.openai.get_ai_response(messages, tools)
            response_message = response.choices[0].message

            if response_message.tool_calls:
                messages.append(response_message)
                await self.handle_tool_call(sender_id, response_message.tool_calls[0], session, messages, lang)
            else:
                final_response_text = response_message.content
                await self.whatsapp.send_message(sender_id, final_response_text)
                await self.db.add_message_to_history(sender_id, 'assistant', final_response_text)

        except Exception as e:
            log.error("Error during message processing.", error=str(e), exc_info=True)
            await self._initiate_human_handoff(sender_id, lang, "فشل فني في النظام.")

    async def handle_tool_call(self, sender_id: str, tool_call: Any, session: UserSession, messages: List[Dict], lang: str):
        function_name = tool_call.function.name
        args = json.loads(tool_call.function.arguments)
        log = logger.bind(user_id=sender_id, tool=function_name, args=args)
        log.info("Handling tool call")

        if function_name == 'initiate_human_handoff':
            reason = args.get('reason', 'طلب المستخدم التحدث إلى موظف')
            details = args.get('details')
            await self._initiate_human_handoff(sender_id, lang, reason, details)

        elif function_name == 'list_services':
            log.info("Listing all available services")
            all_services = self.sheets.get_data('services')
            available_services = [s for s in all_services if str(s.get('is_it_available', '')).lower() == 'نعم']
            await self._send_summary_list(sender_id, session, available_services, "الخدمات المتوفرة", "awaiting_service_choice", lambda item: item.get('service', 'N/A'), lang)

        elif function_name == 'find_service':
            query = args.get('query', '')
            log.info("Finding service with AI-powered search", query=query)

            all_services = self.sheets.get_data('services')
            available_services = [s for s in all_services if str(s.get('is_it_available', '')).lower() == 'نعم']

            if not available_services:
                no_results_text_ar = "عفواً، لا تتوفر لدينا أي خدمات حالياً."
                await self.whatsapp.send_message(sender_id, no_results_text_ar)
                return

            services_json = json.dumps(available_services, ensure_ascii=False)
            filtering_prompt = (
                f"أنت خبير في مطابقة خدمات السفر. مهمتك هي تحليل طلب المستخدم وإيجاد أفضل خدمة مطابقة له من قائمة الخدمات المتوفرة."
                f"\n\n- طلب المستخدم هو: '{query}'"
                f"\n- قائمة الخدمات (JSON): {services_json}"
                f"\n\nالرجاء إعادة قائمة JSON تحتوي *فقط* على الخدمة (أو الخدمات) التي تلبي طلب المستخدم بشكل مباشر. إذا لم تكن هناك خدمة مطابقة تماماً، أعد قائمة فارغة []."
            )

            response = await self.openai.client.chat.completions.create(
                model=self.settings.CHAT_MODEL,
                messages=[{"role": "system", "content": filtering_prompt}],
                response_format={"type": "json_object"},
                temperature=0.0
            )

            matching_services = []
            try:
                response_content = response.choices[0].message.content
                json_match = re.search(r'\[.*\]', response_content, re.DOTALL)
                if json_match:
                    matching_services = json.loads(json_match.group(0))
                else:
                    logger.warning("AI did not return a valid JSON list for service filtering.", raw_response=response_content)
            except (json.JSONDecodeError, IndexError) as e:
                logger.error("Failed to parse AI response for service filtering", error=str(e), raw_response=response.choices[0].message.content)

            if not matching_services:
                no_results_text_ar = "عفواً، لا تتوفر لدينا هذه الخدمة حالياً."
                await self.whatsapp.send_message(sender_id, no_results_text_ar)
                return

            if len(matching_services) == 1:
                response_text_ar = self._format_service_details(matching_services[0])
                await self.whatsapp.send_message(sender_id, response_text_ar)
                await self.db.add_message_to_history(sender_id, 'assistant', response_text_ar)
            else:
                await self._send_summary_list(sender_id, session, matching_services, "الخدمات المطابقة لبحثك", "awaiting_service_choice", lambda item: item.get('service', 'N/A'), lang)

        elif function_name == 'list_offers':
            offers = self.sheets.get_data('offers')
            await self._send_summary_list(sender_id, session, offers, "العروض السياحية", "awaiting_offer_choice", lambda item: item.get('name', 'N/A'), lang)

        elif function_name == 'list_umrah_packages':
            packages = self.sheets.get_data('umrah')
            await self._send_summary_list(sender_id, session, packages, "باقات العمرة", "awaiting_umrah_choice", lambda item: item.get('name_and_type', 'N/A'), lang)

        elif function_name == 'list_flights':
            flights = self.sheets.get_data('flights')
            await self._send_summary_list(sender_id, session, flights, "رحلات الطيران", "awaiting_flight_choice", self._flight_formatter, lang)

        elif function_name == 'get_all_company_info':
            all_info = self.sheets.get_data('informations')
            info_content = json.dumps(all_info, ensure_ascii=False)

            messages.append({"role": "tool", "tool_call_id": tool_call.id, "name": function_name, "content": info_content})

            response = await self.openai.client.chat.completions.create(model=self.settings.CHAT_MODEL, messages=messages)
            response_text = response.choices[0].message.content

            await self.whatsapp.send_message(sender_id, response_text)
            await self.db.add_message_to_history(sender_id, 'assistant', response_text)

        elif function_name == 'find_flights':
            destination = args.get('destination', '')
            origin = args.get('origin', '')
            time_query = args.get('time_query', '')
            destination_normalized = self._normalize_arabic(destination.lower())
            origin_normalized = self._normalize_arabic(origin.lower())
            all_flights = self.sheets.get_data('flights')

            filtered_flights = all_flights
            if destination_normalized:
                if self._is_country_search(destination, all_flights, is_destination=True):
                    filtered_flights = [f for f in filtered_flights if destination_normalized in self._normalize_arabic(str(f.get('to_country', '')).lower())]
                else:
                    filtered_flights = [f for f in filtered_flights if destination_normalized in self._normalize_arabic(str(f.get('destination_airport', '')).lower())]

            if origin_normalized:
                if self._is_country_search(origin, all_flights, is_destination=False):
                    filtered_flights = [f for f in filtered_flights if origin_normalized in self._normalize_arabic(str(f.get('from_country', '')).lower())]
                else:
                    filtered_flights = [f for f in filtered_flights if origin_normalized in self._normalize_arabic(str(f.get('depart_airport', '')).lower())]

            matching_flights = []
            if time_query and filtered_flights:
                flights_json = json.dumps(filtered_flights, ensure_ascii=False)
                today_date = datetime.date.today().isoformat()
                filtering_prompt = (
                    f"أنت خبير في تحليل البيانات. أمامك قائمة رحلات طيران بصيغة JSON. مهمتك هي ترشيح هذه القائمة بناءً على طلب المستخدم الزمني."
                    f"\n\n- تاريخ اليوم هو: {today_date}"
                    f"\n- طلب المستخدم الزمني هو: '{time_query}'"
                    f"\n- بيانات الرحلات: {flights_json}"
                    f"\n\nالرجاء إعادة قائمة JSON تحتوي فقط على الرحلات التي تتطابق بدقة مع طلب المستخدم. إذا لم توجد أي رحلات مطابقة، أعد قائمة فارغة []."
                )

                response = await self.openai.client.chat.completions.create(
                    model=self.settings.CHAT_MODEL,
                    messages=[{"role": "system", "content": filtering_prompt}],
                    response_format={"type": "json_object"},
                    temperature=0.0
                )
                try:
                    response_content = response.choices[0].message.content
                    json_match = re.search(r'\[.*\]', response_content, re.DOTALL)
                    if json_match:
                        matching_flights = json.loads(json_match.group(0))
                    else:
                        logger.warning("AI did not return a valid JSON list for flight filtering.", raw_response=response_content)
                        matching_flights = []
                except (json.JSONDecodeError, IndexError) as e:
                    logger.error("Failed to parse AI response for flight filtering", error=str(e), raw_response=response.choices[0].message.content)
                    matching_flights = []
            else:
                matching_flights = filtered_flights

            title = f"الرحلات القادمة من {origin} إلى {destination}" if origin else f"الرحلات القادمة إلى {destination}"
            await self._send_summary_list(sender_id, session, matching_flights, title, "awaiting_flight_choice", self._flight_formatter, lang)

        elif function_name == 'initiate_visa_discovery':
            topic = args.get('topic')
            all_visas = self.sheets.get_data('visas')
            if topic == 'countries':
                countries = sorted(list(set(v['country'] for v in all_visas if v.get('country'))))
                await self._send_summary_list(sender_id, session, countries, "الدول التي نوفر لها فيزا", "awaiting_visa_country_choice", lambda item: item, lang)
            elif topic == 'types':
                types = sorted(list(set(v['type'] for v in all_visas if v.get('type'))))
                await self._send_summary_list(sender_id, session, types, "أنواع الفيزا المتوفرة", "awaiting_visa_type_choice", lambda item: item, lang)

        elif function_name == 'find_visa_details':
            country_normalized = self._normalize_arabic(args.get('country', '').lower())
            all_visas = self.sheets.get_data('visas')
            visas = [v for v in all_visas if self._normalize_arabic(str(v.get('country', '')).lower()) == country_normalized]

            if not visas:
                no_visa_text_ar = f"عفواً، لا توجد معلومات عن فيزا لدولة *{args.get('country')}*."
                await self.whatsapp.send_message(sender_id, no_visa_text_ar)
                return

            if len(visas) == 1:
                response_text_ar = self._format_visa_details(visas[0])
                await self.whatsapp.send_message(sender_id, response_text_ar)
                await self.db.add_message_to_history(sender_id, 'assistant', response_text_ar)
            else:
                summary_lines = [f"اختر نوع الفيزا لدولة *{args.get('country')}*:", ""]
                for i, visa in enumerate(visas):
                    validity = f"- (صالحة لمدة) {visa['valid_until']}" if visa.get('valid_until') else ""
                    summary_lines.append(f"{i + 1}. {visa.get('type', 'N/A')} {validity}")
                summary_lines.append("\nلمعرفة التفاصيل الكاملة، يرجى إرسال الرقم.")
                response_text_ar = "\n".join(summary_lines)

                final_response_text = response_text_ar
                if lang == 'en':
                    final_response_text = await self._translate_text_for_user(response_text_ar)

                session.context['step'] = 'awaiting_visa_details_choice'
                session.context['data'] = visas
                await self.db.update_user_session(sender_id, session)
                await self.whatsapp.send_message(sender_id, final_response_text)
                await self.db.add_message_to_history(sender_id, 'assistant', final_response_text)

    async def pause_bot_for_user(self, user_number: str):
        await self.db.update_user_session(user_number, UserSession(state='human', context={}))
        logger.info(f"Bot paused for user {user_number} by in-chat command.")
        confirmation_message = "تم إيقاف المساعد الآلي. يمكنك الآن التحدث مباشرة مع الموظف"
        await self.whatsapp.send_message(user_number, confirmation_message)

    async def resume_bot_for_user(self, user_number: str):
        await self.db.update_user_session(user_number, UserSession(state='bot', context={}))
        logger.info(f"Bot resumed for user {user_number} by in-chat command.")
        last_message = await self.db.get_last_user_message_content(user_number)
        lang = self._detect_language(last_message or 'ar')
        resume_message = "المساعد الآلي عاد لخدمتك."
        final_message = resume_message
        if lang == 'en':
            final_message = await self._translate_text_for_user(resume_message)
        await self.whatsapp.send_message(user_number, final_message)
