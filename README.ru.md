# Remnawave Observer: Система защиты от шаринга подписок

![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=for-the-badge&logo=go)
![Docker](https://img.shields.io/badge/Docker-28.0-2496ED?style=for-the-badge&logo=docker)
![Redis](https://img.shields.io/badge/Redis-8.2-DC382D?style=for-the-badge&logo=redis)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)
![Vector](https://img.shields.io/badge/Vector-0.48-orange?style=for-the-badge)

<p align="center">
  🇷🇺 <strong>Русский</strong> | 🇬🇧 <a href="README.md">English</a>
</p>

## Описание

**Remnawave Observer** автоматически обнаруживает и блокирует пользователей, которые делятся своими VPN-подписками.

В отличие от простой блокировки по IP (легко обходится), **Observer блокирует учётную запись пользователя на уровне панели Remnawave** — нарушители теряют подключение **на всех серверах одновременно**, независимо от IP-адреса.

**Ключевые возможности:**

- ✅ Нельзя обойти сменой IP
- ✅ Понимает CGNAT — мобильные операторы с 50+ динамическими IP не триггерят ложные срабатывания
- ✅ Многосигнальный скоринг: география + тип провайдера + плотность IP
- ✅ Глобальная блокировка и разблокировка на всех нодах через Remnawave API

---

## Как это работает

```
Пользователь → Xray → Логи → Vector Agent (нода) → Vector Aggregator → Observer
                                                                             │
                                                       ┌─────────────────────┤
                                                       ▼                     ▼
                                                    Redis              PostgreSQL
                                                 (ASN-пулы,          (история оценок,
                                               расписание разблок.)    журнал событий)
                                                       │
                                                       ▼
                                             Anti-Abuse Scoring
                                        (Гео + тип ASN + плотность IP)
                                                       │
                                                       ▼
                                             Remnawave API
                                         (DisableUser / EnableUser)
```

1. **Vector** на каждой ноде читает логи доступа Xray, извлекает `user_email` (числовой ID) и `source_ip`
2. **Observer** отслеживает уникальные провайдеры (ASN) на пользователя в скользящем временном окне (по умолчанию: 12ч)
3. **Anti-Abuse Scorer** запускает многофакторный анализ при каждом новом ASN-событии
4. **При превышении лимита/оценки** → вызывается `DisableUser()` — аккаунт блокируется глобально
5. **Redis ZSET** планирует автоматическое разблокирование
6. **Scheduler** (каждые 10с) разблокирует истёкшие блокировки через `EnableUser()`

---

## Детектирование

### Режим ASN / провайдер

Группирует все IP одного интернет-провайдера (номер AS) как одну сущность. Один легитимный пользователь обычно подключается максимум с 2–5 провайдеров (домашний ISP + мобильный оператор + возможно рабочий VPN).

**Легитимный пользователь (1 человек):**

```
176.59.40.10  → AS12389 (Ростелеком, дом)
213.87.120.5  → AS8359  (МТС, мобильный)
→ 2 провайдера ✅
```

**Шаринг аккаунта (несколько людей):**

```
176.59.40.10  → AS12389 (Ростелеком, Москва)
91.108.4.50   → AS31200 (Билайн, Казахстан)
185.22.64.10  → AS48642 (Kyivstar, Украина)
→ 3 провайдера в 3 странах ⚠️ → БЛОКИРОВКА
```

База данных ASN автоматически скачивается с [iptoasn.com](https://iptoasn.com) при запуске и обновляется каждый час.

---

## Anti-Abuse Скоринг

Помимо жёсткого лимита по ASN, Observer запускает **конвейер многофакторной оценки**, который рассматривает полную картину перед принятием решения. Это предотвращает ложные срабатывания и обеспечивает постепенное применение мер.

### Конвейер оценки

```
ScoringInput
  ├── Классификации ASN   (тип провайдера + модификатор для каждого ASN)
  ├── Анализ GeoIP        (страны, города, максимальное расстояние между IP)
  ├── Кол-во ASN / лимит
  └── IP на ASN           (плотность IP по провайдеру)
         │
         ▼
   ┌────────────────────────────────────────────────────────────────┐
   │  GeoFeature        вес=50%  → географический разброс          │
   │  ASNFeature        вес=25%  → смешение типов провайдеров      │
   │  CountFeature      вес=15%  → близость к лимиту               │
   │  IPDensityFeature  вес=10%  → количество IP на провайдера     │
   │  ProviderMixFeature мод-р   → усиление при VPN/хостинге       │
   └────────────────────────────────────────────────────────────────┘
         │
         ▼
   FinalScore (0–100)  →  Действие
```

### Описание признаков

| Признак              | Вес    | Что обнаруживает                                                                           |
| -------------------- | ------ | ------------------------------------------------------------------------------------------ |
| `GeoFeature`         | 50%    | Одновременные подключения из разных городов/стран — самый сильный сигнал шаринга           |
| `ASNFeature`         | 25%    | Смешение рискованных типов провайдеров (VPN/хостинг повышает оценку, мобильный снижает)    |
| `CountFeature`       | 15%    | Насколько близко пользователь к лимиту ASN                                                 |
| `IPDensityFeature`   | 10%    | Высокое число уникальных IP на немобильного провайдера (CGNAT мобильные исключены целиком) |
| `ProviderMixFeature` | модиф. | Если >50% VPN/хостинг-провайдеров — устанавливает минимальную оценку 50                    |

### IPDensityFeature — учёт мобильных сетей

Мобильные операторы (CGNAT) вызывают у легитимных пользователей появление 50+ IP за 12-часовое окно. `IPDensityFeature` корректно обрабатывает это:

| Тип провайдера                   | Модификатор | Порог IP для оценки=100        |
| -------------------------------- | ----------- | ------------------------------ |
| Мобильный (МТС, Мегафон)         | ≤ 0.6       | **ПРОПУСК** — исключён целиком |
| Региональный ISP                 | ~0.7        | ~71 IP                         |
| Фиксированный ISP (МГТС, Ростел) | 1.0         | 50 IP                          |
| Корпоративный / Хостинг          | 1.5         | ~33 IP                         |
| VPN / Прокси                     | 1.8         | ~28 IP                         |

Даже при максимальной оценке `IPDensityFeature` вносит лишь **10 баллов** в итоговую оценку. Блокировка (оценка > 75) требует одновременно высокого географического разброса.

### Оценка → Действие

| Оценка | Действие         | Эффект                         |
| ------ | ---------------- | ------------------------------ |
| < 25   | `none`           | Без действий                   |
| 25–44  | `monitor`        | Логируется, без применения мер |
| 45–59  | `warn`           | Отправляется оповещение        |
| 60–74  | `soft_challenge` | Временное ограничение доступа  |
| 75–89  | `temp_disable`   | Аккаунт приостановлен          |
| 90+    | `hard_disable`   | Аккаунт заблокирован           |

Результаты с низкой уверенностью автоматически понижаются на один уровень.

---

## Быстрый старт

### 1. Настройка Observer (центральный сервер)

```bash
git clone https://github.com/dd-devgroup/remnawave-observer.git
cd remnawave-observer/observer_conf

cp docker-compose.example.yml docker-compose.yml
# Отредактируйте .env:
```

**Минимальный `.env`:**

```bash
# --- Обязательные ---
REMNAWAVE_BASE_URL=https://panel.example.com
REMNAWAVE_API_TOKEN=your_api_token_here
POSTGRES_DSN=postgres://observer:password@postgres:5432/observer?sslmode=disable
REDIS_URL=redis://redis:6379/0

# --- Детектирование ---
DETECT_BY_ASN=true
MAX_ASNS_PER_USER=5
USER_ASN_TTL_SECONDS=43200       # Скользящее окно 12 часов

# --- Применение мер ---
BLOCK_DURATION=10m

# --- Скоринг (anti-abuse) ---
SCORING_ENABLED=true

# --- GeoIP ---
GEOIP_ENABLED=true

# --- Исключения ---
EXCLUDED_USERS=admin@example.com,test@example.com
EXCLUDED_IPS=8.8.8.8,1.1.1.1

# --- Уведомления ---
ALERT_WEBHOOK_URL=https://bot.example.com/webhook
```

```bash
docker compose up -d
docker logs observer -f
```

### 2. Настройка Vector (ноды Xray)

Подробное руководство: [**Настройка Vector Agent**](observer/docs/VECTOR-AGENT-SETUP.md)

```bash
cd /opt/xray-node
# Скопируйте vector-node.toml и docker-compose.node.yml из observer_conf/vector-examples/
vim vector-node.toml   # укажите путь к логам + URL Observer aggregator
docker compose -f docker-compose.node.yml up -d
```

**⚠️ Важно:** `user_email` в логах Xray должен быть **числовым ID** (int64), не email-адресом:

```
accepted tcp:1.2.3.4:12345 [inbound_user:12345 >> ...]
                                         ^^^^^ — должно быть числом
```

---

## Справочник конфигурации

### Основные параметры

| Переменная               | Описание                                    | По умолчанию               |
| ------------------------ | ------------------------------------------- | -------------------------- |
| `PORT`                   | HTTP порт прослушивания                     | `9000`                     |
| `POSTGRES_DSN`           | Строка подключения к PostgreSQL             | **обязательно**            |
| `REDIS_URL`              | URL Redis                                   | `redis://localhost:6379/0` |
| `REMNAWAVE_BASE_URL`     | URL панели Remnawave                        | **обязательно**            |
| `REMNAWAVE_API_TOKEN`    | Bearer-токен Remnawave API                  | **обязательно**            |
| `BLOCK_DURATION`         | Длительность блокировки (напр. `10m`, `1h`) | `5m`                       |
| `EXCLUDED_USERS`         | ID пользователей для исключения (через ,)   | —                          |
| `EXCLUDED_IPS`           | IP для исключения (через ,)                 | —                          |
| `EXCLUDED_ASNS`          | ASN для исключения (через ,)                | —                          |
| `ALERT_WEBHOOK_URL`      | URL вебхука для уведомлений о блокировках   | —                          |
| `ALERT_COOLDOWN_SECONDS` | Мин. секунд между оповещениями на юзера     | `3600`                     |

### Детектирование ASN

| Переменная                        | Описание                             | По умолчанию |
| --------------------------------- | ------------------------------------ | ------------ |
| `DETECT_BY_ASN`                   | Включить режим ASN                   | `false`      |
| `MAX_ASNS_PER_USER`               | Макс. уникальных ASN на пользователя | `4`          |
| `USER_ASN_TTL_SECONDS`            | TTL скользящего окна для записей ASN | `3600`       |
| `IPTOASN_UPDATE_INTERVAL_MINUTES` | Интервал обновления базы ASN         | `60`         |

### GeoIP

| Переменная                      | Описание                            | По умолчанию |
| ------------------------------- | ----------------------------------- | ------------ |
| `GEOIP_ENABLED`                 | Включить анализ GeoIP               | `false`      |
| `GEOIP_CACHE_TTL_HOURS`         | TTL кэша GeoIP в памяти             | `24`         |
| `GEO_FALLBACK_ENABLED`          | Включить fallback через 2IP API     | `false`      |
| `TWOIP_TOKEN`                   | Токен 2IP API                       | —            |
| `GEOLITE_ASN_DOWNLOAD_URL`      | URL автозагрузки GeoLite2-ASN.mmdb  | —            |
| `GEOLITE_CITY_DOWNLOAD_URL`     | URL автозагрузки GeoLite2-City.mmdb | —            |
| `GEOLITE_UPDATE_INTERVAL_HOURS` | Интервал автообновления MMDB        | `168` (7д)   |

### Anti-Abuse Скоринг

| Переменная              | Описание                        | По умолчанию |
| ----------------------- | ------------------------------- | ------------ |
| `SCORING_ENABLED`       | Включить конвейер скоринга      | `false`      |
| `SCORE_THRESHOLD_WARN`  | Порог оценки для действия warn  | `45`         |
| `SCORE_THRESHOLD_BLOCK` | Порог оценки для действия block | `75`         |

### Redis / Scheduler

| Переменная                | Описание                              | По умолчанию |
| ------------------------- | ------------------------------------- | ------------ |
| `CLEAR_IPS_DELAY_SECONDS` | Задержка перед очисткой записей IP    | `30`         |
| `REENABLE_TICK_SECONDS`   | Интервал планировщика разблокирования | `10`         |

---

## Мониторинг

Каждые 5 минут Observer выводит сводку ASN-пулов в stdout:

```
[2026-02-27 20:30:42] === ASN POOLS MONITORING START ===
SUMMARY:
   Total active users: 95
   Near limit: 11
   Over limit: 0

TOP USERS BY PROVIDER COUNT (ASN):
    1. [WARN] 12345
       Providers: 5/5 | TTL: 0.6-12.0h
       Score: 47.4 [monitor]
       ASNs: AS3267(11.6h)[1 IPs], AS31133(0.6h)[2 IPs], AS39264(0.7h)[2 IPs], ...
       Geo: countries: RU, cities: Moscow, Samara, Saint Petersburg
       Details:
          AS31133: 2 IP -> 178.176.87.28 -> RU, Samara (53.21, 50.15) [src:mmdb+2ip]
          AS3267:  1 IP -> 82.179.192.10 -> RU, Moscow  (55.74, 37.61) [src:mmdb+2ip]
```

Строка **Score** показывает последнюю anti-abuse оценку и текущее действие.

### Метрики времени выполнения (логируются каждые 60с)

```
[metrics] requests_total=166842 rejected=0 geoip_ok=1353
          rw_disable_ok=12 rw_disable_fail=0 rw_enable_ok=8 rw_enable_fail=0
          uuid_cache_hit=450 uuid_cache_miss=50
```

| Метрика           | Условие оповещения                                      |
| ----------------- | ------------------------------------------------------- |
| `rw_disable_fail` | > 5% попыток блокировок → проверьте Remnawave API       |
| `rw_enable_fail`  | > 0 → пользователи застряли в заблокированном состоянии |
| `uuid_cache_miss` | > 20% → увеличьте `USER_ID_UUID_CACHE_TTL_HOURS`        |

---

## Уведомления через вебхук

Observer отправляет POST-запрос на `ALERT_WEBHOOK_URL` при каждом событии блокировки.

**Payload в режиме ASN:**

```json
{
	"user_identifier": "12345",
	"limit": 4,
	"block_duration": "10m",
	"violation_type": "asn_limit_exceeded",
	"detected_asn_count": 5,
	"asn_details": {
		"AS31133": {
			"asn": "AS31133",
			"organization": "MTS PJSC",
			"ips": ["185.22.64.15", "91.108.4.22"],
			"ip_count": 2
		}
	}
}
```

---

## Тестирование

```bash
# Юнит-тесты
cd observer
go test -count=1 ./...

# Интеграционные тесты (требуется Docker)
docker compose -f docker-compose.test.yml up -d
go test -tags=integration -count=1 ./...
docker compose -f docker-compose.test.yml down
```

---

## Производительность

| Метрика                         | Значение     |
| ------------------------------- | ------------ |
| Пропускная способность Observer | ~5 000 req/s |
| Задержка Remnawave API          | 50–200 мс    |
| Задержка Redis                  | < 5 мс       |
| Накладные расходы Vector (нода) | < 10 МБ RAM  |
| RAM Observer                    | ~150 МБ      |

---

## Безопасность

- Используйте надёжные API-токены (32+ символов)
- TLS для трафика Vector → Observer (через nginx)
- Redis изолирован в Docker-сети, не доступен снаружи
- Настройте rate limiting nginx на эндпоинт Observer
- Используйте секретный путь для `ALERT_WEBHOOK_URL`

---

## Устранение неполадок

**Observer не блокирует пользователей:**

```bash
docker logs observer | grep -i "disable\|error"
# Частые причины:
# - Неверный REMNAWAVE_API_TOKEN
# - REMNAWAVE_BASE_URL недоступен из контейнера
# - user_email содержит email-строку вместо числового ID
```

**Vector не отправляет логи:**

```bash
docker logs vector-agent | grep -i error
# Проверьте, что последняя строка лога содержит числовой user_email:
docker exec vector-agent tail -1 /var/log/xray/access.log
```

**Пользователь не разблокирован:**

```bash
docker logs observer | grep -i "enable\|scheduler"
docker exec redis redis-cli ZRANGE rw:reenable:zset 0 -1 WITHSCORES
```

---

## Документация

- 📖 [Настройка Vector Agent](observer/docs/VECTOR-AGENT-SETUP.md) — подробное руководство по развёртыванию на нодах

---

## Системные требования

**Сервер Observer (центральный):**

- Debian 12+ / Ubuntu 22.04+
- 2 ГБ RAM, 1 vCPU
- Docker 24.0+, Docker Compose v2

**Каждая нода Xray:**

- 512 МБ RAM, 0.25 vCPU (для Vector)
- Docker 24.0+

---

## Лицензия

MIT License
