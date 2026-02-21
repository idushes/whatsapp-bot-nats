# whatsapp-bot-nats

WhatsApp Cloud API ↔ NATS connector. Принимает входящие webhook-события от WhatsApp и публикует их в NATS. Позволяет отправлять сообщения через NATS.

## Запуск

```bash
cp sample.env .env
# Отредактируйте .env — добавьте реальные данные аккаунтов
go run .
```

## Переменные окружения

| Переменная             | Описание                                                     | По умолчанию            |
| ---------------------- | ------------------------------------------------------------ | ----------------------- |
| `NATS_URL`             | Адрес NATS-сервера                                           | `nats://localhost:4222` |
| `WA_<NAME>`            | Аккаунт WhatsApp. Формат: `<phone_number_id>:<access_token>` | —                       |
| `WEBHOOK_VERIFY_TOKEN` | Токен верификации webhook (настраивается в Meta Dashboard)   | —                       |
| `APP_SECRET`           | Facebook App Secret для HMAC SHA256 валидации подписи        | —                       |
| `PORT`                 | Порт HTTP-сервера                                            | `8080`                  |
| `API_VERSION`          | Версия Graph API                                             | `v21.0`                 |

Можно указать несколько аккаунтов:

```env
WA_MY_ACCOUNT=123456789:EAABx...
WA_SUPPORT=987654321:EAABy...
```

## Настройка в Meta Dashboard

1. Создайте приложение на [developers.facebook.com](https://developers.facebook.com/)
2. Добавьте продукт **WhatsApp**
3. В разделе **Configuration → Webhook**:
   - URL: `https://your-server.com/webhook`
   - Verify Token: значение `WEBHOOK_VERIFY_TOKEN` из `.env`
   - Подпишитесь на поле **messages**
4. Скопируйте **App Secret** из Settings → Basic → App Secret в `APP_SECRET`
5. Скопируйте **Phone Number ID** и **Access Token** из WhatsApp → API Setup

## NATS Subjects

### Входящие (WhatsApp → NATS)

| Subject                             | Описание                              |
| ----------------------------------- | ------------------------------------- |
| `whatsapp.<name>.in.webhook`        | Полный webhook payload (raw JSON)     |
| `whatsapp.<name>.in.message`        | Входящее сообщение (любой тип)        |
| `whatsapp.<name>.in.message.<type>` | Сообщение конкретного типа            |
| `whatsapp.<name>.in.status`         | Статус доставки (sent/delivered/read) |
| `whatsapp.<name>.in.error`          | Ошибка от WhatsApp                    |

Возможные `<type>` для `in.message.<type>`:
`text`, `image`, `video`, `audio`, `document`, `sticker`, `location`, `contacts`, `button`, `interactive`, `reaction`

### Исходящие (NATS → WhatsApp)

| Subject                            | Описание                   |
| ---------------------------------- | -------------------------- |
| `whatsapp.<name>.out.sendMessage`  | Отправить текст            |
| `whatsapp.<name>.out.sendImage`    | Отправить изображение      |
| `whatsapp.<name>.out.sendDocument` | Отправить документ         |
| `whatsapp.<name>.out.sendVideo`    | Отправить видео            |
| `whatsapp.<name>.out.sendAudio`    | Отправить аудио            |
| `whatsapp.<name>.out.sendSticker`  | Отправить стикер           |
| `whatsapp.<name>.out.sendLocation` | Отправить локацию          |
| `whatsapp.<name>.out.sendContact`  | Отправить контакт          |
| `whatsapp.<name>.out.sendTemplate` | Отправить template message |
| `whatsapp.<name>.out.sendReaction` | Отправить реакцию          |
| `whatsapp.<name>.out.markRead`     | Пометить как прочитанное   |
| `whatsapp.<name>.out.raw`          | Произвольный API-вызов     |

### Ошибки

| Subject                 | Описание                          |
| ----------------------- | --------------------------------- |
| `whatsapp.<name>.error` | Ошибки API при отправке сообщений |

## Быстрый старт

```bash
# Подписаться на все входящие
nats sub "whatsapp.my_account.in.>"

# Подписаться только на текстовые сообщения
nats sub "whatsapp.my_account.in.message.text"

# Подписаться на статусы доставки
nats sub "whatsapp.my_account.in.status"

# Отправить текстовое сообщение
nats req "whatsapp.my_account.out.sendMessage" '{"to": "79001234567", "text": "Привет!"}'

# Пометить как прочитанное
nats pub "whatsapp.my_account.out.markRead" '{"message_id": "wamid.xxx"}'

# Произвольный API-вызов
nats req "whatsapp.my_account.out.raw" '{"body": {"messaging_product":"whatsapp","to":"79001234567","type":"text","text":{"body":"Raw"}}}'
```

При использовании `nats req` — ответ WhatsApp API вернётся как reply.

---

## Справочник: входящие сообщения (WhatsApp → NATS)

Все примеры ниже — JSON, который приходит в `whatsapp.<name>.in.message`. Формат соответствует [WhatsApp Cloud API](https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks/components).

### Текстовое сообщение

```json
{
  "from": "79001234567",
  "id": "wamid.HBgLMTIzNDU2Nzg5MBUCABIYAL...",
  "timestamp": "1708300000",
  "type": "text",
  "text": {
    "body": "Привет, бот!"
  }
}
```

### Изображение

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300001",
  "type": "image",
  "image": {
    "id": "1234567890",
    "mime_type": "image/jpeg",
    "sha256": "abc123...",
    "caption": "Подпись к фото"
  }
}
```

> Для скачивания медиа используйте Media API: `GET https://graph.facebook.com/v21.0/{media_id}`

### Документ

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300002",
  "type": "document",
  "document": {
    "id": "1234567890",
    "mime_type": "application/pdf",
    "sha256": "def456...",
    "filename": "report.pdf",
    "caption": "Отчёт"
  }
}
```

### Видео

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300003",
  "type": "video",
  "video": {
    "id": "1234567890",
    "mime_type": "video/mp4",
    "sha256": "ghi789..."
  }
}
```

### Аудио

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300004",
  "type": "audio",
  "audio": {
    "id": "1234567890",
    "mime_type": "audio/ogg; codecs=opus",
    "sha256": "jkl012..."
  }
}
```

### Стикер

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300005",
  "type": "sticker",
  "sticker": {
    "id": "1234567890",
    "mime_type": "image/webp",
    "sha256": "mno345..."
  }
}
```

### Локация

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300006",
  "type": "location",
  "location": {
    "latitude": 55.755811,
    "longitude": 37.617617,
    "name": "Красная площадь",
    "address": "Красная площадь, Москва"
  }
}
```

### Контакт

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300007",
  "type": "contacts",
  "contacts": [
    {
      "name": {
        "formatted_name": "Анна Смирнова",
        "first_name": "Анна",
        "last_name": "Смирнова"
      },
      "phones": [{ "phone": "+79001234567", "type": "CELL" }]
    }
  ]
}
```

### Реакция

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300008",
  "type": "reaction",
  "reaction": {
    "message_id": "wamid.HBgL...",
    "emoji": "👍"
  }
}
```

### Ответ на сообщение (context)

```json
{
  "from": "79001234567",
  "id": "wamid.HBgL...",
  "timestamp": "1708300009",
  "type": "text",
  "text": { "body": "Это ответ" },
  "context": {
    "from": "79009876543",
    "id": "wamid.HBgL_original..."
  }
}
```

### Статус доставки (`in.status`)

```json
{
  "id": "wamid.HBgL...",
  "status": "delivered",
  "timestamp": "1708300010",
  "recipient_id": "79001234567",
  "conversation": {
    "id": "conv123",
    "origin": { "type": "user_initiated" },
    "expiration_timestamp": "1708400000"
  },
  "pricing": {
    "billable": true,
    "pricing_model": "CBP",
    "category": "user_initiated"
  }
}
```

Возможные значения `status`: `sent`, `delivered`, `read`, `failed`

---

## Справочник: исходящие запросы (NATS → WhatsApp)

Все примеры — JSON-payload, который нужно отправить в соответствующий `whatsapp.<name>.out.*` subject. Сервис автоматически оборачивает payload в формат WhatsApp Cloud API.

### sendMessage — отправить текст

**Subject:** `whatsapp.<name>.out.sendMessage`

Простой текст:

```json
{ "to": "79001234567", "text": "Привет!" }
```

С предпросмотром ссылки:

```json
{
  "to": "79001234567",
  "text": "Смотри https://example.com",
  "preview_url": true
}
```

Ответ на сообщение:

```json
{ "to": "79001234567", "text": "Это ответ", "reply_to": "wamid.HBgL..." }
```

### sendImage — отправить изображение

**Subject:** `whatsapp.<name>.out.sendImage`

По ссылке:

```json
{
  "to": "79001234567",
  "link": "https://example.com/image.jpg",
  "caption": "Подпись"
}
```

По media ID (ранее загруженное):

```json
{ "to": "79001234567", "media_id": "1234567890", "caption": "Подпись" }
```

### sendDocument — отправить документ

**Subject:** `whatsapp.<name>.out.sendDocument`

```json
{
  "to": "79001234567",
  "link": "https://example.com/report.pdf",
  "caption": "Отчёт за месяц",
  "filename": "report.pdf"
}
```

### sendVideo — отправить видео

**Subject:** `whatsapp.<name>.out.sendVideo`

```json
{
  "to": "79001234567",
  "link": "https://example.com/video.mp4",
  "caption": "Видео"
}
```

### sendAudio — отправить аудио

**Subject:** `whatsapp.<name>.out.sendAudio`

```json
{ "to": "79001234567", "link": "https://example.com/audio.mp3" }
```

### sendSticker — отправить стикер

**Subject:** `whatsapp.<name>.out.sendSticker`

```json
{ "to": "79001234567", "media_id": "1234567890" }
```

### sendLocation — отправить локацию

**Subject:** `whatsapp.<name>.out.sendLocation`

```json
{
  "to": "79001234567",
  "latitude": 55.755811,
  "longitude": 37.617617,
  "name": "Красная площадь",
  "address": "Красная площадь, Москва"
}
```

### sendContact — отправить контакт

**Subject:** `whatsapp.<name>.out.sendContact`

```json
{
  "to": "79001234567",
  "contacts": [
    {
      "name": { "formatted_name": "Анна Смирнова", "first_name": "Анна" },
      "phones": [{ "phone": "+79001234567", "type": "CELL" }]
    }
  ]
}
```

### sendTemplate — отправить template message

**Subject:** `whatsapp.<name>.out.sendTemplate`

```json
{
  "to": "79001234567",
  "template": {
    "name": "hello_world",
    "language": { "code": "ru" },
    "components": [
      {
        "type": "body",
        "parameters": [{ "type": "text", "text": "Иван" }]
      }
    ]
  }
}
```

> Template messages — единственный способ начать диалог с пользователем, который ранее не писал вам. Templates создаются и одобряются в Meta Business Manager.

### sendReaction — отправить реакцию

**Subject:** `whatsapp.<name>.out.sendReaction`

```json
{
  "to": "79001234567",
  "message_id": "wamid.HBgL...",
  "emoji": "👍"
}
```

Убрать реакцию (пустой emoji):

```json
{
  "to": "79001234567",
  "message_id": "wamid.HBgL...",
  "emoji": ""
}
```

### markRead — пометить как прочитанное

**Subject:** `whatsapp.<name>.out.markRead`

```json
{ "message_id": "wamid.HBgL..." }
```

### raw — произвольный API-вызов

**Subject:** `whatsapp.<name>.out.raw`

Прямая отправка тела в WhatsApp API:

```json
{
  "body": {
    "messaging_product": "whatsapp",
    "to": "79001234567",
    "type": "text",
    "text": { "body": "Raw сообщение" }
  }
}
```

Кастомный путь (например, для Media API):

```json
{
  "path": "1234567890",
  "body": {}
}
```

---

## Ошибки

При ошибках API ответ WhatsApp публикуется в `whatsapp.<name>.error`:

```json
{
  "action": "sendMessage",
  "code": 131030,
  "message": "Recipient phone number not in allowed list",
  "type": "OAuthException",
  "request": { "..." }
}
```

При использовании `nats req` ошибка также возвращается как reply.

---

## Docker

```bash
docker build -t whatsapp-bot-nats .
docker run --env-file .env -p 8080:8080 whatsapp-bot-nats
```

## Основные отличия от Telegram-бота

| Аспект         | Telegram                    | WhatsApp Cloud API                                      |
| -------------- | --------------------------- | ------------------------------------------------------- |
| Идентификатор  | Bot Token                   | Phone Number ID + Access Token                          |
| Webhook setup  | `setWebhook` API call       | Настраивается в Meta Dashboard                          |
| Webhook verify | `secret_token` header       | GET с `hub.challenge` + `hub.verify_token`              |
| Payload auth   | Custom header               | `X-Hub-Signature-256` HMAC SHA256                       |
| Send endpoint  | `POST /bot{token}/{method}` | `POST graph.facebook.com/{version}/{phone_id}/messages` |
| Multi-account  | `BOT_<NAME>=token`          | `WA_<NAME>=phone_number_id:access_token`                |
| Endpoint       | `/webhook/<bot_name>`       | `/webhook` (один для всех, роутинг по phone_number_id)  |
