# Remnawave Observer: Система защиты от шаринга подписок

![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=for-the-badge&logo=go)
![Docker](https://img.shields.io/badge/Docker-28.0-2496ED?style=for-the-badge&logo=docker)
![Redis](https://img.shields.io/badge/Redis-8.2-DC382D?style=for-the-badge&logo=redis)
![Vector](https://img.shields.io/badge/Vector-0.48-orange?style=for-the-badge)

<p align="center">
  🇷🇺 <strong>Русский</strong> | 🇬🇧 <a href="README.md">English</a>
</p>

## Описание

**Remnawave Observer** автоматически обнаруживает и блокирует пользователей, которые делятся своими подписками с другими людьми.

В отличие от блокировки по IP (легко обходится), **Observer блокирует учетную запись пользователя на уровне панели Remnawave**. Нарушители теряют подключение **на всех серверах одновременно**, независимо от IP-адреса.

**Ключевые возможности:**

- ✅ Нельзя обойти сменой IP
- ✅ Никаких false positives (shared IP не влияют на других)
- ✅ Глобальная блокировка на всех нодах
- ✅ Автоматическая разблокировка через заданное время
- ✅ Простая архитектура (без брокеров сообщений, без агентов на каждой ноде)

## Как работает

```
Пользователь → Xray → Logs → Vector → Observer → Remnawave API (Disable User)
                                    ↓
                                  Redis (расписание автовключения)
                                    ↓
                              Scheduler → Remnawave API (Enable User)
```

1. **Vector** на каждой ноде читает логи Xray, извлекает `user_email` (numeric ID) и `source_ip`
2. **Observer** подсчитывает уникальные IP/подсети/провайдеры для каждого пользователя
3. **При превышении лимита** → вызывает `Remnawave API DisableUser()` — учетная запись отключается глобально
4. **Redis ZSET** хранит расписание автоматического включения
5. **Scheduler** (каждые 10 секунд) проверяет истекшие блокировки → вызывает `EnableUser()`

## Режимы обнаружения

### 1. По IP-адресам (простой)

Подсчитывает количество уникальных IP-адресов на пользователя.

**Настройка:**

```bash
MAX_IPS_PER_USER=12
```

---

### 2. По подсетям (устойчив к CGNAT)

Группирует IP по подсетям (например, `/24`). Предотвращает ложные срабатывания от мобильных операторов с CGNAT/динамическими IP.

**Пример:**

```
185.22.64.10 + 185.22.64.25 → обе в подсети 185.22.64.0/24 → 1 подсеть
185.22.64.10 + 91.108.4.50  → разные подсети → 2 подсети
```

**Настройка:**

```bash
DETECT_BY_SUBNET=true
MAX_SUBNETS_PER_USER=4
SUBNET_MASK_IPV4=24
```

---

### 3. По ASN / провайдерам (🌟 рекомендуется)

**Самый точный режим.** Группирует все IP одного интернет-провайдера (AS number) как единую сущность.

**Пример легитимного использования (1 человек):**

```
176.59.40.10  → AS12389 (Ростелеком дом)
213.87.120.5  → AS8359 (МТС мобильный)
→ 2 провайдера ✅
```

**Пример шаринга (несколько человек):**

```
176.59.40.10  → AS12389 (Ростелеком Москва)
91.108.4.50   → AS31200 (Билайн Казахстан)
185.22.64.10  → AS48642 (Kyivstar Украина)
→ 3 провайдера ⚠️ → БЛОКИРОВКА
```

**Настройка:**

```bash
DETECT_BY_ASN=true
MAX_ASNS_PER_USER=4
```

База ASN автоматически скачивается с [iptoasn.com](https://iptoasn.com) при запуске.

## Быстрый старт

### 1. Настройка Observer (центральный сервер)

```bash
git clone https://github.com/dd-devgroup/remnawave-observer.git
cd remnawave-observer/observer_conf

cp .env.example .env
vim .env
```

**Минимальная конфигурация .env:**

```bash
# Режим обнаружения (выберите один)
MAX_IPS_PER_USER=12              # Режим по IP (или)
# DETECT_BY_SUBNET=true           # Режим по подсетям (или)
DETECT_BY_ASN=true                # Режим по ASN (рекомендуется)
MAX_ASNS_PER_USER=4

# Remnawave API
REMNAWAVE_API_URL=https://panel.example.com
REMNAWAVE_API_TOKEN=your_api_token

# Длительность блокировки
BLOCK_DURATION=10m

# Исключения
EXCLUDED_USERS=admin,test
EXCLUDED_IPS=8.8.8.8

# Webhook (опционально)
ALERT_WEBHOOK_URL=https://bot.example.com/webhook
```

```bash
docker-compose up -d
docker logs observer-remna -f
```

### 2. Настройка Vector (ноды с Xray)

См. детальное руководство: [**Vector Agent Setup**](observer/docs/VECTOR-AGENT-SETUP.md)

**Быстрые шаги:**

```bash
cd /opt/xray-node
cp observer_conf/vector-examples/* ./
vim vector-node.toml  # Настроить путь к логам и URL Observer
docker-compose -f docker-compose.node.yml up -d
```

**⚠️ Критически важно:** `user_email` в логах должен содержать **numeric ID** (int64), а не email:

```
accepted tcp:1.2.3.4:12345 [inbound_user:12345 >> ...]
                                        ^^^^^ — numeric ID
```

## Справочник конфигурации

### Основные переменные

| Переменная             | Описание                                 | По умолчанию |
| ---------------------- | ---------------------------------------- | ------------ |
| `MAX_IPS_PER_USER`     | Лимит IP                                 | 12           |
| `DETECT_BY_SUBNET`     | Включить режим по подсетям               | false        |
| `MAX_SUBNETS_PER_USER` | Лимит подсетей                           | 3            |
| `SUBNET_MASK_IPV4`     | Маска подсети                            | 24           |
| `DETECT_BY_ASN`        | Включить режим по ASN                    | false        |
| `MAX_ASNS_PER_USER`    | Лимит провайдеров                        | 4            |
| `BLOCK_DURATION`       | Длительность блокировки                  | 5m           |
| `REMNAWAVE_API_URL`    | URL панели                               | обязательно  |
| `REMNAWAVE_API_TOKEN`  | API токен                                | обязательно  |
| `EXCLUDED_USERS`       | Исключенные пользователи (через запятую) | —            |
| `EXCLUDED_IPS`         | Исключенные IP                           | —            |
| `EXCLUDED_SUBNETS`     | Исключенные подсети                      | —            |
| `EXCLUDED_ASNS`        | Исключенные ASN                          | —            |
| `ALERT_WEBHOOK_URL`    | URL для webhook                          | —            |

### Настройки Redis

| Переменная                | Описание           | По умолчанию |
| ------------------------- | ------------------ | ------------ |
| `USER_IP_TTL_SECONDS`     | TTL записей        | 3600         |
| `CLEAR_IPS_DELAY_SECONDS` | Задержка очистки   | 30           |
| `REENABLE_TICK_SECONDS`   | Интервал scheduler | 10           |

## Webhook уведомления

Observer отправляет POST запросы на `ALERT_WEBHOOK_URL` при блокировке пользователей.

### Примеры payload

**Режим ASN:**

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

**Режим по IP:**

```json
{
	"user_identifier": "12345",
	"detected_ips_count": 13,
	"limit": 12,
	"all_user_ips": ["1.1.1.1", "2.2.2.2"],
	"block_duration": "10m",
	"violation_type": "ip_limit_exceeded"
}
```

**Режим по подсетям:**

```json
{
	"user_identifier": "12345",
	"detected_ips_count": 4,
	"limit": 3,
	"all_user_ips": ["185.22.64.0/24", "91.108.4.0/24"],
	"block_duration": "10m",
	"violation_type": "subnet_limit_exceeded"
}
```

## Мониторинг

### Метрики (логируются каждые 60 секунд)

```
[Metrics] requests_total=1523 rejected=5 geoip_ok=1480
[Metrics] rw_disable_ok=12 rw_disable_fail=0 rw_enable_ok=8 rw_enable_fail=0
[Metrics] uuid_cache_hit=450 uuid_cache_miss=50
```

**Ключевые метрики:**

- `rw_disable_ok/fail` — блокировки пользователей (успешные/неудачные)
- `rw_enable_ok/fail` — автоматические разблокировки (успешные/неудачные)
- `uuid_cache_hit/miss` — эффективность кеша UUID (цель: >80% hits)
- `rejected` — отклоненные запросы

### Алерты

```
rw_disable_fail / (rw_disable_ok + rw_disable_fail) > 0.05  # >5%
→ Проверьте доступность Remnawave API

rw_enable_fail > 0
→ Пользователи застряли в заблокированном состоянии!

uuid_cache_miss / (uuid_cache_hit + uuid_cache_miss) > 0.2  # >20%
→ Увеличьте TTL
```

## Тестирование

```bash
# Unit-тесты
cd observer
go test -count=1 ./...

# Integration-тесты
docker-compose -f docker-compose.test.yml up -d
go test -tags=integration -count=1 ./...
docker-compose -f docker-compose.test.yml down
```

**Покрытие:** 75+ тестов во всех пакетах ✅

## Производительность

| Метрика                         | Значение        |
| ------------------------------- | --------------- |
| Пропускная способность Observer | ~5000 req/s     |
| Latency Remnawave API           | 50-200 ms       |
| Latency Redis                   | <5 ms           |
| Overhead Vector                 | <10 MB RAM/ноду |
| RAM Observer                    | ~150 MB         |

### Оптимизация

**Высоконагруженные системы:**

```toml
# vector-node.toml
batch.max_events = 500
batch.timeout_secs = 2
compression = "gzip"
```

**Горизонтальное масштабирование:**

```bash
# Запустите несколько инстансов Observer за load balancer
# Используйте единый общий Redis для всех инстансов
```

## Безопасность

**Рекомендации:**

- Используйте сильные API токены (32+ символа)
- HTTPS для Vector → Observer
- Redis изолирован в Docker-сети
- Nginx rate limiting
- Секретные webhook URLs

**Минимальные права:**

- Observer: не требуется root
- Vector: read-only монтирование логов
- Redis: не exposed наружу

## Устранение неполадок

### Observer не блокирует пользователей

```bash
# Проверьте API
curl -H "Authorization: Bearer TOKEN" https://panel.example.com/api/users/1

# Проверьте логи
docker logs observer-remna | grep -i disable

# Частые причины:
# - Неверный REMNAWAVE_API_TOKEN
# - API недоступен
# - user_email содержит email вместо numeric ID
```

### Vector не отправляет данные

```bash
docker logs vector-agent | grep -i error
docker exec vector-agent tail -1 /var/log/xray/access.log
# Должен показать user_email как число!
```

### Пользователь не разблокировался

```bash
docker logs observer-remna | grep Scheduler
docker exec redis-obs redis-cli ZRANGE rw:reenable:zset 0 -1 WITHSCORES
docker logs observer-remna | grep rw_enable_fail
```

## Документация

- 📖 [Vector Agent Setup Guide](observer/docs/VECTOR-AGENT-SETUP.md) — детальное развёртывание на нодах
- 🏗️ [ADR-007: Архитектура](observer/docs/adr/ADR-007-remnawave-user-enforcement.md) — архитектурное решение
- 🧪 [N8N Updater Playbook](observer/docs/N8N-UPDATER-PLAYBOOK.md) — автоматизация geodata

## Системные требования

**Observer (центральный):**

- Debian 13 / Ubuntu 24.04
- 2 GB RAM, 1 vCPU
- Docker 28.0+

**Node (каждая нода с Xray):**

- Debian 13 / Ubuntu 24.04
- 1 GB RAM, 0.25 vCPU (Vector)
- Docker 28.0+

## Лицензия

MIT License
