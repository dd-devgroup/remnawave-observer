# Phase 4 Manual Testing Checklist

После деплоя Phase 4 commits (M–R) выполнить следующие проверки:

## 1. HTTP Server Timeouts (Commit M)

### Базовые проверки
```bash
# Проверить что сервис запустился с новыми таймаутами
docker logs observer_service | grep "HTTP Server таймауты"

# Ожидаемый вывод:
# HTTP Server таймауты: ReadHeader=5s Read=15s Write=15s Idle=60s MaxHeaderBytes=1048576
```

### Функциональные тесты

**Тест 1: Нормальный запрос**
```bash
curl -X POST http://localhost:9000/log-entry \
  -H "Content-Type: application/json" \
  -d '[{"user_email":"test@example.com","source_ip":"192.168.1.1"}]'

# Ожидается: 202 Accepted
```

**Тест 2: Медленное чтение (slowloris protection)**
```bash
# Открыть соединение и медленно отправлять данные (>15s)
# Ожидается: соединение закроется по ReadTimeout через 15s
(echo -n 'POST /log-entry HTTP/1.1\r\nHost: localhost:9000\r\n'; sleep 20) | nc localhost 9000

# Ожидается: соединение закрыто, нет висящих goroutines
```

**Тест 3: Большие заголовки**
```bash
# Отправить огромный заголовок (>1MB)
curl -X POST http://localhost:9000/log-entry \
  -H "X-Large-Header: $(python3 -c 'print("A"*2000000)')" \
  -d '[]'

# Ожидается: 431 Request Header Fields Too Large или connection reset
```

## 2. Mandatory Publish + Returns (Commit N)

### Проверка нормальной работы
```bash
# Убедиться что publishes успешны (exchange с bindings существует)
curl -X POST http://localhost:9000/log-entry \
  -H "Content-Type: application/json" \
  -d '[{"user_email":"user@test.com","source_ip":"10.0.0.1"},{"user_email":"user@test.com","source_ip":"10.0.0.2"},{"user_email":"user@test.com","source_ip":"10.0.0.3"}]'

# Проверить логи observer
docker logs observer_service | grep -E "(Сообщение возвращено|PublishBlockMessage)"

# Ожидается: НЕТ "Сообщение возвращено брокером" — все messages routed успешно
```

### Проверка unroutable detection (опционально)
```bash
# Временно удалить binding из RabbitMQ UI (http://localhost:15672)
# Отправить запрос → должен триггерить блок → publishes будут retried с логами "unroutable"
# Восстановить binding

# Ожидаемые логи:
# "Сообщение возвращено брокером (unroutable): ... (попытка X/Y)"
```

## 3. SeqNo Confirms (Commit O)

### Проверка отсутствия goroutine leak
```bash
# Запустить нагрузку (~100 requests)
for i in {1..100}; do
  curl -s -X POST http://localhost:9000/log-entry \
    -H "Content-Type: application/json" \
    -d '[{"user_email":"user'$i'@test.com","source_ip":"10.0.0.'$i'"}]' &
done
wait

# Проверить количество goroutines (должно вернуться к baseline)
curl -s http://localhost:9000/debug/pprof/goroutine?debug=1 | grep "goroutine profile"

# Или проверить через метрики если доступны
# Ожидается: goroutines count не растёт с количеством publishes
```

### Проверка confirms успешны
```bash
# Проверить metrics (если counters dumper работает)
docker logs observer_service | grep -E "RabbitPublishSuccess|RabbitPublishFail"

# Ожидается: RabbitPublishSuccess растёт, RabbitPublishFail = 0
```

## 4. Strict JSON Mode (Commit P)

### Тест 1: Strict=false (default) — extra fields OK
```bash
# По умолчанию STRICT_JSON_DECODE=false
curl -X POST http://localhost:9000/log-entry \
  -H "Content-Type: application/json" \
  -d '[{"user_email":"test@example.com","source_ip":"192.168.1.1","extra_field":"should be ignored","timestamp":"2026-02-06"}]'

# Ожидается: 202 Accepted (extra fields игнорируются)
```

### Тест 2: Strict=true — extra fields rejected
```bash
# Временно установить STRICT_JSON_DECODE=true в .env, перезапустить observer
# docker-compose restart observer_service

curl -X POST http://localhost:9000/log-entry \
  -H "Content-Type: application/json" \
  -d '[{"user_email":"test@example.com","source_ip":"192.168.1.1","unknown_field":"reject"}]'

# Ожидается: 400 Bad Request с кодом "invalid_json" или "unknown_field"

# Вернуть STRICT_JSON_DECODE=false, перезапустить
```

## 5. Integration Tests (Commit Q)

### Запуск локальных integration tests
```bash
cd observer
make test-integration

# Ожидается:
# - Docker compose up (redis + rabbitmq)
# - Tests pass (SCAN + publish)
# - Docker compose down
# - Вывод: "Integration tests completed successfully"
```

### Проверка что unit tests не сломались
```bash
cd observer
go test -count=1 ./...

# Ожидается: все тесты зелёные, без FAIL
```

## 6. Мониторинг Production

### Метрики для отслеживания
- **HTTP server**: connection count, request latency (не должны вырасти значительно)
- **RabbitMQ**: `RabbitPublishSuccess`, `RabbitPublishFail`, `RabbitPublishRetry` counters
- **Goroutines**: runtime goroutine count (не должен расти с нагрузкой)
- **Memory**: RSS не должен расти после нагрузки (нет leak)

### Логи для мониторинга
```bash
# HTTP таймауты (при старте)
docker logs observer_service | grep "HTTP Server таймауты"

# Unroutable messages (в production не должно быть)
docker logs observer_service | grep "возвращено брокером"

# Confirms (должны быть только success)
docker logs observer_service | grep "RabbitPublish"

# Строгая валидация JSON (если включена)
docker logs observer_service | grep "Строгая валидация JSON"
```

## 7. Reconnect Scenario (комплексная проверка)

### Тест устойчивости к разрывам соединения
```bash
# Шаг 1: Запустить нагрузку в фоне
for i in {1..200}; do
  curl -s -X POST http://localhost:9000/log-entry \
    -H "Content-Type: application/json" \
    -d '[{"user_email":"user'$i'@test.com","source_ip":"10.0.0.'$((i%250+1))'"}]' &
  sleep 0.5
done

# Шаг 2: Перезапустить RabbitMQ во время нагрузки
docker restart rabbitmq

# Шаг 3: Дождаться завершения нагрузки
wait

# Шаг 4: Проверить логи observer
docker logs observer_service | tail -100

# Ожидается:
# - "Соединение с RabbitMQ потеряно. Попытка переподключения..."
# - "Успешное (пере)подключение к RabbitMQ"
# - Retries с backoff
# - Финально: большинство publishes successful (возможны failed из-за window)
```

## 8. Config Validation

### Проверить что все новые env vars загружаются
```bash
# Проверить .env файл содержит новые переменные
cat observer_conf/.env | grep -E "(HTTP_|STRICT_JSON)"

# Должны быть:
# HTTP_READ_HEADER_TIMEOUT_SECONDS
# HTTP_READ_TIMEOUT_SECONDS
# HTTP_WRITE_TIMEOUT_SECONDS
# HTTP_IDLE_TIMEOUT_SECONDS
# HTTP_MAX_HEADER_BYTES
# STRICT_JSON_DECODE
```

### Проверить дефолты если переменные не установлены
```bash
# Закомментировать HTTP_* переменные в .env
# Перезапустить observer
# Проверить логи — должны быть дефолты:

docker logs observer_service | grep "HTTP Server таймауты"
# Ожидается: ReadHeader=5s Read=15s Write=15s Idle=60s MaxHeaderBytes=1048576
```

## Критерии успеха Phase 4

- ✅ HTTP server timeouts активны и защищают от slowloris
- ✅ Mandatory publish детектирует unroutable messages
- ✅ Confirms работают без goroutine leak (stable goroutine count)
- ✅ Strict JSON mode работает (опционально)
- ✅ Integration tests проходят
- ✅ Reconnect scenario успешен (publish resume после разрыва)
- ✅ Все unit tests зелёные
- ✅ Нет регрессий в latency/memory

## Rollback Plan

При проблемах откатить коммиты в обратном порядке:

```bash
git revert 6fce13f  # COMMIT R (docs)
git revert 6fce13f  # COMMIT Q (integration tests)
git revert d6f3ba5  # COMMIT P (strict JSON)
git revert f8ba532  # COMMIT O (seqNo confirms)
git revert 255e851  # COMMIT N (mandatory publish)
git revert 25fe607  # COMMIT M (HTTP timeouts)
```

Каждый коммит независим и может быть откачен отдельно.
