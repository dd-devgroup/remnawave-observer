# Remnawave Observer: Система защиты от шаринга подписок

![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=for-the-badge&logo=go)
![Docker](https://img.shields.io/badge/Docker-28.0-2496ED?style=for-the-badge&logo=docker)
![Redis](https://img.shields.io/badge/Redis-8.2-DC382D?style=for-the-badge&logo=redis)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)
![Panel](https://img.shields.io/badge/Panel%20Ingest-2.7.0-blue?style=for-the-badge)

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
Пользователь → Xray / Remnawave Node → Remnawave Panel → panel poller в Observer
                                                                      │
                                               ┌──────────────────────┴──────────────────────┐
                                               ▼                                             ▼
                                            Redis                                       PostgreSQL
                                      (hot-window, sightings,                     (история, score,
                                       расписание разблок.)                        журнал событий)
                                               │
                                               ▼
                                Anti-Abuse Scoring + Enforcement
                        (DisableUser + временный node-side block IP)
```

1. **Observer** сам опрашивает `Remnawave Panel 2.7.0+` и забирает per-user IP по активным нодам
2. Poller нормализует каждое наблюдение в прежний внутренний формат `user_email` (числовой ID) + `source_ip`
3. **Observer** отслеживает уникальные провайдеры (ASN) на пользователя в скользящем временном окне
4. **Anti-Abuse Scorer** запускает многофакторный анализ при каждом новом ASN-событии
5. **Только при blocking score action** → вызывается `DisableUser()`, а offending IP могут временно блокироваться на нодах, где они были замечены
6. **Redis ZSET** планирует автоматическое разблокирование, а устаревшие наблюдения истекают автоматически

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

Observer использует **конвейер многофакторной оценки**, который рассматривает полную картину перед принятием решения. Это предотвращает ложные срабатывания и оставляет блокировки только за score action, а не за голый подсчет ASN.

### Конвейер оценки

```
ScoringInput
  ├── Классификации ASN   (тип провайдера + модификатор для каждого ASN)
  ├── Анализ GeoIP        (страны, города, максимальное расстояние между IP)
  └── IP на ASN           (плотность IP по провайдеру)
         │
         ▼
   ┌────────────────────────────────────────────────────────────────┐
   │  GeoFeature        вес=55%  → географический разброс          │
   │  ASNFeature        вес=30%  → смешение типов провайдеров      │
   │  IPDensityFeature  вес=15%  → количество IP на провайдера     │
   │  ProviderMixFeature мод-р   → усиление при VPN/хостинге       │
   └────────────────────────────────────────────────────────────────┘
         │
         ▼
   FinalScore (0–100)  →  Действие
```

### Описание признаков

| Признак              | Вес    | Что обнаруживает                                                                           |
| -------------------- | ------ | ------------------------------------------------------------------------------------------ |
| `GeoFeature`         | 55%    | Одновременные подключения из разных городов/стран — самый сильный сигнал шаринга           |
| `ASNFeature`         | 30%    | Смешение рискованных типов провайдеров (VPN/хостинг повышает оценку, мобильный снижает)    |
| `IPDensityFeature`   | 15%    | Высокое число уникальных IP на немобильного провайдера (CGNAT мобильные исключены целиком) |
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

# --- Базовое поведение ---
BLOCK_DURATION=10m
USER_ASN_TTL_SECONDS=43200
GEOIP_ENABLED=true
SCORE_THRESHOLD_WARN=50.0
SCORE_THRESHOLD_BLOCK=85.0

# --- Исключения ---
EXCLUDED_USERS=admin@example.com,test@example.com
EXCLUDED_IPS=8.8.8.8,1.1.1.1
EXCLUDED_INTERNAL_SQUAD_UUIDS=

# --- Уведомления ---
ALERT_WEBHOOK_URL=https://bot.example.com/webhook
```

Panel-only ingest, scoring и временный node-side IP block уже включены по умолчанию. Дополнительные override добавляйте только если реально хотите уйти от стандартного поведения.

```bash
docker compose up -d
docker logs observer -f
```

### 2. Требования к Remnawave

Для panel-only ingest нужны:

- `Remnawave Panel >= 2.7.0`
- `Remnawave Node >= 2.7.0`
- `cap_add: NET_ADMIN` на нодах, если включён локальный node-side IP block

По умолчанию Observer больше не требует разворачивать Vector на каждой ноде. Старый путь через Vector остаётся как legacy-режим через `LOG_SOURCE_MODE=http|hybrid`.

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
| `LOG_SOURCE_MODE`        | Режим ingest: `panel`, `http`, `hybrid`     | `panel`                    |
| `PANEL_POLL_INTERVAL_SECONDS` | Интервал опроса панели                 | `60`                       |
| `PANEL_FETCH_TIMEOUT_SECONDS` | Таймаут одного fetch-job               | `20`                       |
| `PANEL_FETCH_RESULT_POLL_SECONDS` | Интервал polling результата      | `2`                        |
| `PANEL_FETCH_MAX_INFLIGHT` | Макс. количество одновременных fetch-job | `3`                        |
| `NODE_EXECUTOR_BLOCK_ENABLED` | Включить временный node-side block IP | `true`                   |
| `BLOCK_DURATION`         | Длительность блокировки (напр. `10m`, `1h`) | `5m`                       |
| `EXCLUDED_USERS`         | ID пользователей для исключения (через ,)   | —                          |
| `EXCLUDED_IPS`           | IP для исключения (через ,)                 | —                          |
| `EXCLUDED_INTERNAL_SQUAD_UUIDS` | UUID Internal Squads, исключённых из anti-sharing | —      |
| `EXCLUDED_ASNS`          | ASN для исключения (через ,)                | —                          |
| `ALERT_WEBHOOK_URL`      | URL вебхука для уведомлений о блокировках   | —                          |
| `ALERT_COOLDOWN_SECONDS` | Мин. секунд между оповещениями на юзера     | `3600`                     |

### Горячее окно провайдеров

| Переменная                        | Описание                             | По умолчанию |
| --------------------------------- | ------------------------------------ | ------------ |
| `MAX_ASNS_PER_USER`               | Только legacy-порог для мониторинга; не участвует в блокировке и scoring | `4` |
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
   Monitor actions: 11
   Warn or challenge actions: 3
   Blocking actions: 0

TOP USERS BY PROVIDER COUNT AND SCORE:
    1. [MONITOR] 12345
       Providers: 5 | TTL: 0.6-12.0h
       Score: 47.4 [monitor]
       ASNs: AS3267(11.6h)[1 IPs], AS31133(0.6h)[2 IPs], AS39264(0.7h)[2 IPs], ...
       Geo: countries: RU, cities: Moscow, Samara, Saint Petersburg
       Details:
          AS31133: 2 IP -> 178.176.87.28 -> RU, Samara (53.21, 50.15) [src:mmdb+2ip]
          AS3267:  1 IP -> 82.179.192.10 -> RU, Moscow  (55.74, 37.61) [src:mmdb+2ip]
```

Строка **Score** показывает последнюю anti-abuse оценку и текущее действие. Количество провайдеров в мониторинге теперь только информационное и само по себе не приводит к бану.

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

Observer отправляет POST-запрос на `ALERT_WEBHOOK_URL` при каждом scoring alert с действием `warn` и выше.

**Scoring payload:**

```json
{
	"user_identifier": "12345",
	"violation_type": "scoring_action",
	"score": 82.4,
	"score_action": "temp_disable",
	"block_duration": "10m",
	"all_user_asns": ["AS31133", "AS3267"],
	"asn_details": {
		"AS31133": {
			"asn": "AS31133",
			"organization": "MTS PJSC",
			"ips": ["185.22.64.15", "91.108.4.22"],
			"ip_count": 2
		}
	},
	"score_breakdown": [
		{ "name": "geo", "score": 90, "weight": 0.55, "confidence": 0.9 },
		{ "name": "asn", "score": 70, "weight": 0.30, "confidence": 0.8 }
	]
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
| Накладные расходы legacy Vector (`http` режим) | < 10 МБ RAM  |
| RAM Observer                    | ~150 МБ      |

---

## Безопасность

- Используйте надёжные API-токены (32+ символов)
- TLS для запросов Remnawave Panel → Observer API
- Если используется legacy-режим `Vector`, дополнительно защитите трафик Vector → Observer
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

**Legacy-режим Vector не отправляет логи:**

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

- 📖 [Настройка Vector Agent](observer/docs/VECTOR-AGENT-SETUP.md) — legacy-руководство для `LOG_SOURCE_MODE=http|hybrid`

---

## Системные требования

**Сервер Observer (центральный):**

- Debian 12+ / Ubuntu 22.04+
- 2 ГБ RAM, 1 vCPU
- Docker 24.0+, Docker Compose v2

**Ноды Remnawave:**

- Remnawave Node 2.7.0+
- `cap_add: NET_ADMIN`, если включён временный node-side block IP
- Для legacy-режима `Vector` дополнительно нужны ~512 МБ RAM, 0.25 vCPU и Docker 24.0+

---

## Лицензия

MIT License
