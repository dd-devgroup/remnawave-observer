# B6: Observability (метрики + логирование EventID)

## Проблема
- Нет метрик для мониторинга работы Blocker
- EventID из Observer не логируется (уже исправлено в B1)

## Решение

### Метрики (atomic counters)
Создан `metrics/counters.go` с глобальными счетчиками:

**RabbitMQ:**
- `MessagesReceived` — всего получено сообщений
- `MessagesAck` — успешно обработано (ACK)
- `MessagesNack` — отклонено (NACK requeue=false)

**NFT commands:**
- `NftCommandsTotal` — всего команд выполнено
- `NftCommandsSuccess` — успешных команд
- `NftCommandsFail` — failed команд (включая timeout)
- `NftCommandsTimeout` — timeout команд (subset of fail)

**Validation:**
- `InvalidIPsCount` — пропущено невалидных IP

**Worker pool:**
- `PoolQueueFullWarnings` — сколько раз очередь была >90% заполнена

### Metrics dumper
- Запускается в main.go: `metrics.StartDumper(l, 60*time.Second)`
- Каждые 60 секунд логирует все счетчики
- Формат: `[Metrics] RabbitMQ: recv=X ack=Y nack=Z | NFT: total=A ok=B fail=C timeout=D | ...`

### Логирование EventID
Уже реализовано в B1 через `logEventContext()`:
- Формат: `[EventID=abc123]` или `[EventID=abc123, Chunk=1/3]`
- Добавляется к каждому INFO/WARNING/ERROR логу в processor.go

## Изменения
- `metrics/counters.go`: 9 atomic счетчиков + StartDumper
- `processor.go`: интеграция метрик (invalid IPs, nft commands, queue full)
- `worker.go`: интеграция метрик (messages ack/nack)
- `main.go`: запуск metrics dumper

## Использование метрик
```go
metrics.Get().MessagesReceived.Add(1)
metrics.Get().NftCommandsSuccess.Add(1)
count := metrics.Get().MessagesAck.Load()
```

## Дедупликация
**Не реализовано** в B6 (можно добавить позже):
- EventID можно использовать для дедупликации
- Хранить processed EventID+ChunkIndex в Redis с TTL
- Или локальный LRU cache (требует зависимость)

**Обоснование пропуска:**
- Blocker idempotent: повторное добавление IP в nftables безопасно
- Простота важнее: без новых deps, без state management
- В будущем легко добавить через Redis SET

## Мониторинг
### Полезные метрики для alerts
- `MessagesNack / MessagesReceived > 0.1` — много ошибок
- `NftCommandsTimeout / NftCommandsTotal > 0.05` — nftables медленный
- `PoolQueueFullWarnings > 0` — backpressure, нужно увеличить workers

### Логи
Все логи содержат EventID для trace-ability:
```
INFO - Обработка 10 валидных IP/CIDR из 12. [EventID=abc123, Chunk=2/3]
ERROR - TIMEOUT при обработке IP 1.2.3.4 (превышен лимит 3s). [EventID=abc123, Chunk=2/3]
```

## Риски
- **Низкий**: только добавление метрик, не меняет логику
- **Rollback**: удалить metrics package и вызовы

## Будущие улучшения
1. Prometheus exporter (HTTP /metrics endpoint)
2. Дедупликация через Redis
3. Structured logging (JSON format)
