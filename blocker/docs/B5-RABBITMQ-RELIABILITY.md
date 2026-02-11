# B5: RabbitMQ reliability (QoS, ack/nack стратегии)

## Проблема
- Жестко закодированный prefetch=1 (неоптимально для worker pool)
- Нужна правильная стратегия ack/nack для разных типов ошибок

## Решение

### QoS Prefetch
- Конфиг `PREFETCH_COUNT` (default: auto = BLOCKER_WORKERS * 2)
- Consumer передает prefetch в QoS
- Backpressure: RabbitMQ не отправит больше сообщений, чем prefetch

### Ack/Nack стратегия (уже была правильной)
- **ACK**: успешная обработка всех валидных IP
- **NACK requeue=false**: любая ошибка (poison или transient)
  - Poison message (невалидный JSON, все IP невалидны) → не зациклит
  - Transient error (nft timeout) → отправится в DLQ (если настроен)

**Обоснование NACK requeue=false:**
- Безопасно: не зациклит очередь мусором
- Простота: не нужен retry logic с x-death headers
- DLQ: если настроен, сообщения попадут туда для manual review
- В будущем можно добавить retry limit через x-death

### Reconnect
- Уже реализован в worker.go (цикл с reconnectDelay)
- При ошибке соединения: Close → wait 5s → retry Connect

## Изменения
- `config.go`: +PrefetchCount (auto = BlockerWorkers * 2)
- `consumer.go`: +prefetchCount field, использование в Qos()
- `worker.go`: передача cfg.PrefetchCount в NewConsumer

## Поведение
### Manual ack mode (autoAck=false)
- Уже был включен в Consume()
- handleMessage делает Ack/Nack явно

### Prefetch расчет
```go
BLOCKER_WORKERS=16 → PrefetchCount=32
```
Позволяет RabbitMQ pre-load сообщения, чтобы воркеры не простаивали.

## Конфигурация
```env
PREFETCH_COUNT=0    # 0 = auto (BLOCKER_WORKERS * 2)
BLOCKER_WORKERS=16  # влияет на prefetch
```

## Риски
- **Низкий**: только изменение prefetch, логика ack/nack осталась
- **Rollback**: вернуть prefetch=1 в consumer.go

## Будущие улучшения
1. Retry logic с x-death headers (ограниченное количество requeue)
2. DLQ обработка (manual review poison messages)
3. Метрики ack/nack ratio
