const { Client, LocalAuth } = require('whatsapp-web.js');
const express = require('express');
const axios = require('axios');
const qrcode = require('qrcode-terminal');
const rateLimit = require('express-rate-limit');
const pino = require('pino');
const { parsePhoneNumberFromString } = require('libphonenumber-js');

const logger = pino({ level: process.env.LOG_LEVEL || 'info' });
const app = express();
app.use(express.json());

const BOT_WEBHOOK_URL = process.env.BOT_WEBHOOK_URL || 'http://al3sal_app:8000/webhook';
const BOT_RESUME_URL = process.env.BOT_RESUME_URL || 'http://al3sal_app:8000/internal/resume';
const BOT_RESUME_COMMAND = process.env.BOT_RESUME_COMMAND || '/start';
const BOT_PAUSE_URL = process.env.BOT_PAUSE_URL || 'http://al3sal_app:8000/internal/pause';
const BOT_PAUSE_COMMAND = process.env.BOT_PAUSE_COMMAND || '/stop';
const INTERNAL_API_KEY = process.env.INTERNAL_API_KEY;

if (!INTERNAL_API_KEY) {
    logger.fatal("FATAL ERROR: INTERNAL_API_KEY is not defined. The service cannot run securely.");
    process.exit(1);
}

const apiKeyAuth = (req, res, next) => {
    const userApiKey = req.get('X-API-Key');
    if (userApiKey && userApiKey === INTERNAL_API_KEY) {
        next();
    } else {
        logger.warn('Unauthorized API call attempt');
        res.status(401).json({ error: 'Unauthorized' });
    }
};

const sendMessageLimiter = rateLimit({
    windowMs: 60 * 1000,
    max: 20,
    message: { error: 'Too many messages sent, please wait a minute.' },
    standardHeaders: true,
    legacyHeaders: false,
});

const client = new Client({
    authStrategy: new LocalAuth(),
    puppeteer: { headless: true, args: ['--no-sandbox', '--disable-setuid-sandbox'] }
});

client.on('qr', qr => {
    logger.info('QR Code Received, scan it with your phone.');
    qrcode.generate(qr, { small: true });
});

client.on('ready', () => {
    logger.info('WhatsApp client is ready!');
});

client.on('disconnected', (reason) => {
    logger.warn({ reason }, 'Client was logged out');
});

async function forwardMessageWithRetries(payload) {
    const maxRetries = 3;
    const retryDelay = 2000;
    for (let i = 0; i < maxRetries; i++) {
        try {
            await axios.post(BOT_WEBHOOK_URL, payload, {
                headers: { 'X-API-Key': INTERNAL_API_KEY }
            });
            logger.info({ to: BOT_WEBHOOK_URL, from: payload.from_number }, 'Message forwarded successfully');
            return;
        } catch (error) {
            logger.error({ attempt: i + 1, error: error.message }, 'Attempt to forward message failed');
            if (i < maxRetries - 1) {
                await new Promise(resolve => setTimeout(resolve, retryDelay));
            } else {
                logger.error('All attempts to forward message failed. No message sent to user.');
            }
        }
    }
}

client.on('message', async msg => {
    if (msg.body && !msg.fromMe) {
        logger.info({ from: msg.from, body: msg.body }, 'Received message');
        const payload = {
            from_number: msg.from.split('@')[0],
            body: msg.body
        };
        await forwardMessageWithRetries(payload);
    }
});

client.on('message_create', async msg => {
    if (!msg.fromMe || !msg.body) {
        return;
    }
    const command = msg.body.toLowerCase();
    const user_number = msg.to.split('@')[0];

    if (command === BOT_PAUSE_COMMAND.toLowerCase()) {
        logger.info({ user: user_number }, 'Pause command detected');
        try {
            await axios.post(BOT_PAUSE_URL, { user_number }, {
                headers: { 'X-API-Key': INTERNAL_API_KEY }
            });
            logger.info({ user: user_number }, 'Pause command sent to main app');
        } catch (error) {
            logger.error({ user: user_number, error: error.message }, 'Error sending pause command');
        }
    } else if (command === BOT_RESUME_COMMAND.toLowerCase()) {
        logger.info({ user: user_number }, 'Resume command detected');
        try {
            await axios.post(BOT_RESUME_URL, { user_number }, {
                headers: { 'X-API-Key': INTERNAL_API_KEY }
            });
            logger.info({ user: user_number }, 'Resume command sent to main app');
        } catch (error) {
            logger.error({ user: user_number, error: error.message }, 'Error sending resume command');
        }
    }
});


app.post('/send-message', apiKeyAuth, sendMessageLimiter, async (req, res) => {
    logger.info({ body: req.body }, 'Received /send-message request payload');
    const { number, message } = req.body;
    if (!number || !message) {
        logger.warn('Send message request missing number or message');
        return res.status(400).json({ error: 'Missing number or message' });
    }
    try {
        let numberToValidate = number;
        if (!numberToValidate.startsWith('+')) {
            numberToValidate = `+${numberToValidate}`;
        }
        const phoneNumber = parsePhoneNumberFromString(numberToValidate);
        if (!phoneNumber || !phoneNumber.isValid()) {
            logger.warn({ number: numberToValidate }, 'Invalid phone number format provided to API');
            return res.status(400).json({ error: 'Invalid phone number format' });
        }
        const numberToSend = phoneNumber.countryCallingCode + phoneNumber.nationalNumber;
        await client.sendMessage(`${numberToSend}@c.us`, message);
        logger.info({ to: numberToSend }, 'Message sent successfully via API');
        res.status(200).json({ status: 'Message sent' });
    } catch (error) {
        logger.error({ to: number, error: error.message }, 'Failed to send message via API');
        res.status(500).json({ error: 'Failed to send message' });
    }
});

app.get('/health', async (req, res) => {
    try {
        const state = await client.getState();
        if (state === 'CONNECTED') {
            res.status(200).json({ status: 'ok', whatsapp_state: state });
        } else {
            logger.warn({ state }, 'Health check failed: WhatsApp not connected');
            res.status(503).json({ status: 'error', whatsapp_state: state || 'UNKNOWN' });
        }
    } catch (error) {
        logger.error(error, 'Health check threw an exception');
        res.status(500).json({ status: 'error', message: 'Failed to get WhatsApp client state.' });
    }
});

client.initialize();
const port = process.env.PORT || 3000;
const server = app.listen(port, () => logger.info(`Bridge service listening on port ${port}`));

const gracefulShutdown = () => {
    logger.info('Shutting down gracefully...');
    server.close(() => {
        logger.info('HTTP server closed.');
        client.destroy()
            .then(() => {
                logger.info('WhatsApp client destroyed.');
                process.exit(0);
            })
            .catch(e => {
                logger.error(e, 'Error destroying client during shutdown.');
                process.exit(1);
            });
    });
};

process.on('SIGINT', gracefulShutdown);
process.on('SIGTERM', gracefulShutdown);
