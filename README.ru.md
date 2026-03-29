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

**Remnawave Observer** это anti-sharing сервис поверх Remnawave на уровне панели.

Он больше не зависит от `Vector` как от основной схемы. Главный источник данных теперь это **Remnawave Panel 2.7.0+**: Observer сам опрашивает панель, получает per-user IP по нодам, нормализует события во внутренний формат, считает score и только после этого принимает решение о блокировке пользователя.

Текущее поведение:

- основной ingest-режим это `panel`
- блокировки теперь **только scoring-only**
- голый подсчет ASN **не банит**
- пользователи из заданных `Internal Squads` полностью обходят anti-sharing
- мусорные IP вроде `0.0.0.0` и `::` тихо выкидываются и дополнительно чистятся из Redis/PostgreSQL на старте
- `HWID` и история запросов подписки (`SRH`) используются как слой против false positive
- legacy `/log-entry` и режим `Vector` по-прежнему доступны через `LOG_SOURCE_MODE=http|hybrid`

---

## Как это работает

```text
Пользователь -> Xray / Remnawave Node -> Remnawave Panel -> panel poller в Observer
                                                           |
                                +--------------------------+--------------------------+
                                |                                                     |
                              Redis                                               PostgreSQL
                 (hot window, sightings, watermarks,                      (история, score events,
                  cooldown алертов, расписание re-enable)                  anti-abuse actions)
                                |
                                v
                     Anti-Abuse Scoring + Enforcement
                 (DisableUser + опциональный node-side block IP)
```

Пайплайн:

1. Observer опрашивает активные ноды через Remnawave panel jobs.
2. Ответ `fetch-users-ips` превращается в внутренние события `user_email=<numeric internal ID>` и `source_ip`.
3. Unspecified IP (`0.0.0.0`, `::`) отбрасываются сразу.
4. Пользователи из `EXCLUDED_INTERNAL_SQUAD_UUIDS`, `EXCLUDED_USERS`, `EXCLUDED_IPS` и `EXCLUDED_ASNS` обходят обработку.
5. Panel observations дедуплицируются по watermark `(nodeUUID, userID, ip, lastSeen)`.
6. В Redis обновляется hot-window по ASN, после чего запускается scoring.
7. Только `temp_disable` и `hard_disable` запускают enforcement:
   - глобальный `DisableUser` через Remnawave
   - опциональный временный node-side block IP через `POST /api/node-plugins/executor`
8. Redis scheduler автоматически делает `EnableUser` после `BLOCK_DURATION`.

Важно: для executor не нужно заранее создавать отдельный node plugin. Нужны только `Remnawave Panel >= 2.7.0`, `Remnawave Node >= 2.7.0` и `cap_add: NET_ADMIN` на нодах.

---

## Скоринг

Observer теперь работает в режиме **scoring-only**. Превышение `MAX_ASNS_PER_USER` не банит пользователя и не влияет на score напрямую.

### Базовые сигналы

| Сигнал | Вес | Что означает |
| --- | ---: | --- |
| `GeoFeature` | 55% | Географический разлет между наблюдаемыми IP |
| `ASNFeature` | 30% | Смесь типов провайдеров: mobile снижает риск, VPN/hosting повышает |
| `IPDensityFeature` | 15% | Плотность уникальных IP внутри non-mobile провайдеров |
| `ProviderMixFeature` | модификатор | Поднимает нижнюю границу score, если доминируют VPN/hosting |

### Evidence из Remnawave

После базового score Observer при необходимости дополнительно берет из Remnawave:

- `HWID` devices
- subscription request history (`SRH`)

Эти данные не добавляют новый штраф сами по себе. Они используются как анти-false-positive слой:

- сильная single-device консистентность может снизить итоговый score на `40%`
- низкое разнообразие устройств и user-agent может снизить итоговый score на `20%`

То есть `HWID` и `SRH` могут понизить действие с `temp_disable` до `warn`, но не создают отдельный бан сами по себе.

### Действия по score по умолчанию

`SCORE_THRESHOLD_WARN` и `SCORE_THRESHOLD_BLOCK` управляют границами `warn` и `hard_disable`. С текущими дефолтами получается:

| Score | Действие | Реальный эффект |
| ---: | --- | --- |
| `< 25` | `none` | Ничего не делать |
| `25-49` | `monitor` | Сохранение и отображение в мониторинге |
| `50-59` | `warn` | Webhook alert |
| `60-74` | `soft_challenge` | Повышенный alert state, без disable |
| `75-84` | `temp_disable` | Disable пользователя + опциональный node-side block IP |
| `85+` | `hard_disable` | Disable пользователя + опциональный node-side block IP |

Результаты с низкой уверенностью автоматически понижаются на один уровень.

---

## Быстрый старт

### 1. Настройка Observer

```bash
git clone https://github.com/dd-devgroup/remnawave-observer.git
cd remnawave-observer/observer_conf

cp docker-compose.example.yml docker-compose.yml
```

Минимальный `.env`:

```bash
POSTGRES_DSN=postgres://observer:password@postgres:5432/observer?sslmode=disable
REDIS_URL=redis://redis:6379/0

REMNAWAVE_BASE_URL=https://panel.example.com
REMNAWAVE_API_TOKEN=your_api_token_here
LOG_SOURCE_MODE=panel

BLOCK_DURATION=10m
USER_ASN_TTL_SECONDS=43200
GEOIP_ENABLED=true
SCORE_THRESHOLD_WARN=50.0
SCORE_THRESHOLD_BLOCK=85.0

EXCLUDED_USERS=
EXCLUDED_IPS=
EXCLUDED_INTERNAL_SQUAD_UUIDS=
EXCLUDED_ASNS=

ALERT_WEBHOOK_URL=https://bot.example.com/webhook
```

Запуск:

```bash
docker compose up -d
docker logs observer -f
```

### 2. Требования к Remnawave

- `Remnawave Panel >= 2.7.0`
- `Remnawave Node >= 2.7.0`
- `cap_add: NET_ADMIN` на нодах, если `NODE_EXECUTOR_BLOCK_ENABLED=true`

`Vector` больше не нужен для стандартного деплоя. Legacy ingest остается доступен через `LOG_SOURCE_MODE=http|hybrid`.

---

## Справочник конфигурации

### Основное

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `PORT` | HTTP порт | `9000` |
| `POSTGRES_DSN` | Строка подключения к PostgreSQL | обязательно |
| `REDIS_URL` | URL Redis | `redis://localhost:6379/0` |
| `REMNAWAVE_BASE_URL` | URL панели Remnawave | обязательно |
| `REMNAWAVE_API_TOKEN` | API токен Remnawave | обязательно |
| `REMNAWAVE_HEADER` | Опциональный reverse-proxy gate header в формате `KEY=VALUE` | пусто |
| `LOG_SOURCE_MODE` | Режим ingest: `panel`, `http`, `hybrid` | `panel` |
| `REMNAWAVE_TIMEOUT_SECONDS` | Таймаут запросов к Remnawave API | `5` |
| `USERID_UUID_CACHE_TTL_HOURS` | TTL кэша internal ID -> UUID | `24` |
| `BLOCK_DURATION` | Длительность disable | `5m` |
| `ALERT_WEBHOOK_URL` | Webhook для алертов | пусто |
| `ALERT_COOLDOWN_SECONDS` | Cooldown алертов на пользователя | `3600` |

### Panel ingest

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `PANEL_POLL_INTERVAL_SECONDS` | Интервал основного опроса | `60` |
| `PANEL_FETCH_TIMEOUT_SECONDS` | Таймаут одного `fetch-users-ips` job | `20` |
| `PANEL_FETCH_RESULT_POLL_SECONDS` | Интервал polling результата | `2` |
| `PANEL_FETCH_MAX_INFLIGHT` | Максимум параллельных fetch job | `3` |
| `NODE_EXECUTOR_BLOCK_ENABLED` | Включить временный node-side block IP | `true` |

### Исключения и hot window

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `EXCLUDED_USERS` | Список internal user ID через запятую | пусто |
| `EXCLUDED_IPS` | Список IP или CIDR через запятую | пусто |
| `EXCLUDED_INTERNAL_SQUAD_UUIDS` | UUID Internal Squad, полностью исключенных из anti-sharing | пусто |
| `EXCLUDED_ASNS` | Список ASN через запятую | пусто |
| `USER_ASN_TTL_SECONDS` | TTL hot-window по ASN | `86400` |
| `MAX_ASNS_PER_USER` | Legacy monitoring-only порог; не влияет на score и ban | `4` |
| `CLEAR_IPS_DELAY_SECONDS` | Задержка перед очисткой ASN hot-window после disable | `30` |

### Scoring и GeoIP

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `GEOIP_ENABLED` | Включить GeoIP enrichment | `false` |
| `GEOIP_CACHE_TTL_HOURS` | TTL GeoIP cache | `24` |
| `GEO_FALLBACK_ENABLED` | Включить fallback через 2IP | `false` |
| `TWOIP_TOKEN` | Токен 2IP | пусто |
| `TWOIP_BASE_URL` | Base URL 2IP API | `https://api.2ip.io` |
| `GEOLITE_ASN_PATH` | Путь к GeoLite ASN MMDB | `/app/data/GeoLite2-ASN.mmdb` |
| `GEOLITE_CITY_PATH` | Путь к GeoLite City MMDB | `/app/data/GeoLite2-City.mmdb` |
| `SCORE_THRESHOLD_WARN` | Порог `warn` | `50` |
| `SCORE_THRESHOLD_BLOCK` | Порог `hard_disable` | `85` |

### Необязательное learning и logging

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `UNKNOWN_PROVIDERS_LOG_ENABLED` | Логировать неизвестные provider classification | `false` |
| `AUTO_LEARNING_ENABLED` | Включить provider auto-learning | `false` |
| `AUTO_LEARNING_INTERVAL_HOURS` | Интервал auto-learning | `24` |
| `AUTO_LEARNING_MIN_COUNT` | Минимум совпадений для learned keyword | `10` |
| `AUTO_LEARNING_MIN_CONFIDENCE` | Минимальная уверенность: `high`, `medium`, `low` | `high` |
| `AUTO_LEARNING_MAX_ADDS_PER_RUN` | Максимум добавлений за цикл | `20` |
| `AUTO_LEARNING_OUTPUT_FILE` | Имя выходного overlay файла | `providers.learned.yaml` |
| `AUTO_LEARN_MIN_DISTINCT_USERS` | Мин. число distinct users для Postgres-backed learning | `3` |
| `AUTO_LEARN_AUTO_APPROVE_THRESHOLD` | Порог auto-approve | `0.8` |

---

## Мониторинг и метрики

Каждые 5 минут Observer печатает в stdout summary по hot-window провайдеров. Количество ASN там теперь **только информационное**. Реальное enforcement-решение определяется последним scoring action.

Runtime-метрики логируются каждые 60 секунд:

```text
[metrics] requests_total=166842 rejected=0 geoip_ok=1353 geoip_fail=0 geoip_timeout=0
          rw_disable_ok=12 rw_disable_fail=0 rw_enable_ok=8 rw_enable_fail=0
          uuid_cache_hit=450 uuid_cache_miss=50 user_resolve_cache_hit=120 user_resolve_cache_miss=6
          panel_submit_ok=94 panel_submit_fail=0 panel_result_ok=94 panel_result_fail=0 panel_timeout=0 panel_no_data=3 panel_dedup_hit=211
          executor_block_ok=4 executor_block_fail=0 discarded_unspecified_ip=17 excluded_squad_users=25
```

Самые полезные счетчики:

- `rw_disable_fail`, `rw_enable_fail`: здоровье enforcement через Remnawave
- `panel_submit_fail`, `panel_result_fail`, `panel_timeout`: проблемы panel ingest
- `panel_dedup_hit`: повторные snapshots, отброшенные watermark-логикой
- `discarded_unspecified_ip`: шум `0.0.0.0` / `::`
- `excluded_squad_users`: пользователи, исключенные по Internal Squad

---

## Webhook Payload

Алерты отправляются для `warn` и выше.

```json
{
  "user_identifier": "12345",
  "violation_type": "scoring_action",
  "score": 82.4,
  "score_action": "temp_disable",
  "score_confidence": 0.88,
  "block_duration": "10m",
  "all_user_asns": ["AS31133", "AS3267"],
  "score_modifiers": ["hwid_srh_single_device_consistency"],
  "score_breakdown": [
    { "name": "geo", "score": 90, "weight": 0.55, "confidence": 0.9 },
    { "name": "asn", "score": 70, "weight": 0.30, "confidence": 0.8 },
    { "name": "hwid_evidence", "score": 0, "weight": 0, "confidence": 0.9 }
  ],
  "geo_analysis": {
    "unique_countries": ["RU", "DE"],
    "unique_cities": ["Moscow", "Berlin"],
    "agglomerations": [],
    "max_distance_km": 1608.0,
    "geo_score": 90,
    "geo_flags": ["cross_border"]
  }
}
```

---

## Устранение неполадок

**Нет данных из панели**

Проверь:

- `REMNAWAVE_BASE_URL` и `REMNAWAVE_API_TOKEN`
- `Remnawave Panel/Node >= 2.7.0`
- метрики `panel_submit_fail`, `panel_result_fail`, `panel_timeout`

**Пользователь не блокируется**

Смотри последний score action в мониторинге или в `user_score_events`.

- `monitor`, `warn`, `soft_challenge` не делают disable
- только `temp_disable` и `hard_disable` запускают enforcement

**Некоторые пользователи вообще не попадают в anti-sharing**

Проверь:

- `EXCLUDED_INTERNAL_SQUAD_UUIDS`
- `EXCLUDED_USERS`
- `EXCLUDED_IPS`
- `EXCLUDED_ASNS`

Пользователи из исключенных Internal Squad обходят scoring, persistence, alerts и enforcement целиком.

**Не видно трафика с `0.0.0.0` или `::`**

Это нормально. Unspecified IP отбрасываются на ingest-этапе и удаляются из hot-window/history при startup cleanup.

**Legacy Vector не шлет логи**

Актуально только для `LOG_SOURCE_MODE=http|hybrid`:

```bash
docker logs vector-agent | grep -i error
docker exec vector-agent tail -1 /var/log/xray/access.log
```

**Не работает node-side block IP**

Проверь:

- `NODE_EXECUTOR_BLOCK_ENABLED=true`
- на node-контейнерах есть `cap_add: NET_ADMIN`
- метрику `executor_block_fail`

---

## Дополнительная документация

- [observer/docs/VECTOR-AGENT-SETUP.md](observer/docs/VECTOR-AGENT-SETUP.md) — legacy-настройка Vector для `LOG_SOURCE_MODE=http|hybrid`

---

## Системные требования

Сервер Observer:

- Debian 12+ / Ubuntu 22.04+
- 2 ГБ RAM
- 1 vCPU
- Docker 24.0+ и Docker Compose v2

Сторона Remnawave:

- `Remnawave Panel >= 2.7.0`
- `Remnawave Node >= 2.7.0`
- `cap_add: NET_ADMIN`, если включен временный node-side block IP

Legacy-режим `Vector` дополнительно требует per-node agent ресурсы и больше не является основной архитектурой.

---

## Лицензия

MIT License
