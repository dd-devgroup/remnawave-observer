# Blocker Worker — Manual Checklist

## Обзор
Blocker Worker слушает RabbitMQ очередь с событиями блокировки от Observer и добавляет IP/CIDR в nftables blacklist.

## Архитектура
- **RabbitMQ Consumer** → получает сообщения с IP списками
- **Worker Pool** (bounded) → ограничивает concurrency
- **NFT Executor** → безопасное выполнение nft команд
- **Metrics** → мониторинг работы

## Конфигурация (.env)
```env
# RabbitMQ
RABBITMQ_URL=amqp://guest:guest@localhost/
PREFETCH_COUNT=0  # 0 = auto (BLOCKER_WORKERS * 2)

# Worker pool
BLOCKER_WORKERS=16       # Количество параллельных nft операций
BLOCKER_QUEUE_SIZE=1000  # Размер буфера задач

# NFT timeouts
NFT_TIMEOUT_SECONDS=3  # Timeout на одну nft команду
```

## Запуск локально
```bash
cd blocker/
go build -o blocker-worker ./cmd/blocker_worker
./blocker-worker
```

## Проверка работы
### 1. Логи должны показывать
```
INFO - Запуск Blocker Worker...
INFO - Metrics dumper запущен (интервал: 60s)
INFO - Worker pool запущен: 16 воркеров, очередь 1000
INFO - Соединение с RabbitMQ успешно установлено.
INFO - QoS установлен: prefetchCount=32
INFO - [*] Воркер готов. Ожидание сообщений о блокировке...
```

### 2. При получении сообщения
```
INFO - Обработка 5 валидных IP/CIDR из 5. [EventID=abc123]
INFO - Команда 'nft add element inet firewall user_blacklist { 1.2.3.4 timeout 5m }' выполнена успешно.
```

### 3. Метрики (каждые 60s)
```
[Metrics] RabbitMQ: recv=10 ack=9 nack=1 | NFT: total=50 ok=48 fail=2 timeout=1 | Validation: invalid_ips=3 | Pool: queue_full_warnings=0
```

### 4. Проверка nftables
```bash
sudo nft list set inet firewall user_blacklist
# Должны увидеть добавленные IP с timeout
```

## Тестирование
```bash
# Unit тесты
go test ./... -v

# Только processor тесты
go test ./internal/processor -v

# С таймаутом
go test ./... -timeout 30s
```

## Graceful Shutdown
```bash
# Отправить SIGTERM
kill -TERM <pid>

# Или SIGINT (Ctrl+C)
```

**Что происходит:**
1. Stop consuming (новые сообщения не принимаются)
2. Worker pool drain (10s timeout)
3. Текущие nft операции завершаются
4. Exit

## Troubleshooting

### Много NACK
```
[Metrics] nack=100
```
**Причины:**
- Невалидный JSON в сообщениях → проверить Observer
- Все IP невалидны → проверить формат данных
- NFT команды падают → проверить `sudo nft list ruleset`

### Много timeouts
```
[Metrics] timeout=50
```
**Причины:**
- NFT_TIMEOUT_SECONDS слишком маленький → увеличить до 5s
- Система перегружена → проверить CPU/memory
- nftables проблемы → `dmesg | grep nft`

### Queue full warnings
```
WARNING - Очередь worker pool почти заполнена: 950/1000
```
**Решение:**
- Увеличить BLOCKER_WORKERS (больше concurrency)
- Увеличить BLOCKER_QUEUE_SIZE (больше буфер)
- Проверить почему nft команды медленные

### RabbitMQ reconnect loop
```
WARNING - Не удалось подключиться к RabbitMQ: dial tcp...
```
**Решение:**
- Проверить RABBITMQ_URL
- Проверить что RabbitMQ запущен
- Проверить сеть/firewall

## Rollback стратегия
Все коммиты B1–B6 независимы:
- **B1**: Удалить validation package, вернуть старый isValidIPOrCIDR
- **B2**: Удалить CommandRunner interface, вернуть прямой exec
- **B3**: Удалить pool, вернуть unbounded goroutines (не рекомендуется)
- **B4**: Удалить context.WithTimeout, вернуть прямой вызов
- **B5**: Вернуть prefetch=1 в consumer.go
- **B6**: Удалить metrics package и вызовы

## Безопасность
- ✅ IP validation через netip (no injection)
- ✅ exec.CommandContext (no shell injection)
- ✅ Bounded worker pool (no goroutine leak)
- ✅ Timeouts на все nft операции
- ✅ Manual ack mode (no message loss)
- ✅ Graceful shutdown

## Производительность
- **Workers=16** → 16 параллельных nft команд
- **Prefetch=32** → RabbitMQ pre-loads сообщения
- **QueueSize=1000** → backpressure buffer
- **NFT timeout=3s** → защита от зависаний

## Мониторинг метрик
Каждые 60s в логах:
```
[Metrics] RabbitMQ: recv=X ack=Y nack=Z | NFT: total=A ok=B fail=C timeout=D | ...
```

**Нормальные значения:**
- `ack / recv > 0.95` (>95% успешных)
- `timeout / total < 0.01` (<1% timeouts)
- `queue_full_warnings = 0` (нет backpressure)

## Интеграция с Observer
Blocker совместим с Observer Phase 1 PR8:
- Парсит EventID, ChunkIndex, ChunkTotal, SchemaVersion (omitempty)
- Логирует EventID для trace-ability
- Обрабатывает chunked события (каждый chunk независимо)
