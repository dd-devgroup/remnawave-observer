# B3 — Worker pool + bounded queue + graceful shutdown

## Изменения

### 1. Конфигурация worker pool
- **Файл**: `internal/config/config.go`
- **Новые параметры**:
  - `BLOCKER_WORKERS` (default: 16) — количество воркеров
  - `BLOCKER_QUEUE_SIZE` (default: 1000) — размер буфера очереди
  - `DrainTimeout` (hardcoded: 10s) — таймаут на drain при shutdown
- **Валидация**: отрицательные/нулевые значения заменяются на defaults

### 2. Worker pool реализация
- **Файл**: `internal/processor/pool.go`
- **Структура**: `WorkerPool` с bounded buffered channel
- **Методы**:
  - `Start()` — запускает N воркеров
  - `Submit(task)` — блокирующая отправка задачи
  - `TrySubmit(task)` — неблокирующая попытка отправки
  - `Shutdown()` — graceful shutdown с drain
  - `QueueLen()/QueueCap()` — мониторинг очереди
- **Безопасность**: `defer recover()` в Submit/TrySubmit для защиты от panic

### 3. Graceful shutdown
- **Логика**:
  1. `close(taskQueue)` — новые задачи отклоняются
  2. Ждем `wg.Wait()` с timeout (DrainTimeout)
  3. Если timeout — `cancel()` для force stop
  4. Ждем еще 100ms grace period
- **Защита от зависаний**: воркеры, которые долго выполняются, прерываются через ctx.Done()

### 4. Интеграция в processor
- **Файл**: `internal/processor/processor.go`
- **Изменения**:
  - `MessageProcessor` теперь содержит `pool *WorkerPool`
  - `Process()` отправляет задачи в pool вместо spawn горутин
  - Backpressure logging: если очередь >90% заполнена
  - Прерывание обработки при shutdown pool

### 5. Main и Worker
- **Файлы**: `cmd/blocker_worker/main.go`, `internal/worker/worker.go`
- **Изменения**:
  - Создание и запуск pool в main()
  - Передача pool в Worker
  - Worker.Run() вызывает `pool.Shutdown()` при завершении

### 6. Тесты
- **Файл**: `internal/processor/pool_test.go`
- **Покрытие**:
  - `TestWorkerPool_BasicExecution` — простое выполнение задач
  - `TestWorkerPool_Concurrency` — ограничение concurrency
  - `TestWorkerPool_QueueFull` — поведение при заполненной очереди
  - `TestWorkerPool_GracefulShutdown` — корректное завершение задач
  - `TestWorkerPool_ShutdownTimeout` — force stop при timeout
  - `TestWorkerPool_SubmitAfterShutdown` — отклонение задач после shutdown
  - `TestWorkerPool_ContextCancellation` — отмена через ctx

## Архитектура

### До рефакторинга
```
RabbitMQ message
    → Process()
    → for each IP:
        spawn goroutine (UNLIMITED!)
            → nft command
```

**Проблема**: 100,000 IP = 100,000 goroutines = OOM

### После рефакторинга
```
RabbitMQ message
    → Process()
    → for each IP:
        Submit task to pool (BOUNDED queue)
            ↓
    Worker Pool (N workers)
        worker 1 ←┐
        worker 2  │ fetch tasks from queue
        ...       │
        worker N ←┘
            → nft command
```

**Защита**: max N горутин + bounded queue + backpressure

### Поток Shutdown
```
SIGINT/SIGTERM
    → worker.cancel()
    → pool.Shutdown()
        1. close(taskQueue) — reject new tasks
        2. wait with timeout
        3. if timeout: cancel ctx → force stop
        4. grace period 100ms
    → exit
```

## Backpressure стратегия

### Поведение при заполнении очереди
1. `Submit()` **блокирует** отправителя (RabbitMQ consumer)
2. RabbitMQ consumer перестает забирать новые сообщения
3. RabbitMQ QoS prefetch ограничивает буферизацию
4. Upsteam (Observer) получает backpressure через RabbitMQ

### Мониторинг
- Если `QueueLen() > QueueCap() * 90%` → WARNING в логах
- Логируется EventID для отслеживания проблемных сообщений

## Риски и откат

### Риски
- **Средний**: изменяется concurrency модель
- Если tasks долго выполняются → могут накапливаться в очереди
- Если queue переполнен → блокировка consumer (но это backpressure, не баг)

### Откат (rollback)
1. Revert коммита B3
2. Удалить `pool.go` и `pool_test.go`
3. Вернуть старую версию `processor.go`, `main.go`, `worker.go`, `config.go`
4. Тест: `go build ./... && go test ./...`

## Тестирование

### Unit тесты
```bash
cd blocker
go test ./internal/processor -v -run TestWorkerPool
```

Ожидаемый результат: 7 тестов PASS

### Интеграционное тестирование

#### 1. Нормальная нагрузка
Отправить сообщение с 100 IP:
```json
{"ips": ["192.168.1.1", ...], "duration": "5m"}
```
Ожидание: обработка за ~10-20s (зависит от nft performance)

#### 2. Высокая нагрузка
Отправить 10 сообщений по 100 IP каждое (1000 IP total).
Ожидание:
- Логи WARNING о заполнении очереди
- Обработка без OOM
- Все IP добавлены в nft

#### 3. Graceful shutdown
Во время обработки большого массива IP:
1. Отправить SIGINT (Ctrl+C)
2. Наблюдать логи: "останавливаем worker pool..."
3. Ожидание: задачи завершаются gracefully, exit code 0

#### 4. Проверка concurrency
```bash
# В отдельном терминале
watch -n 0.5 'ps -eLf | grep blocker_worker | wc -l'
```
Ожидание: количество потоков ~= BLOCKER_WORKERS + overhead (не 100,000+)

## Конфигурация

### Рекомендуемые значения

**Для малых нагрузок** (< 100 IP/s):
```env
BLOCKER_WORKERS=8
BLOCKER_QUEUE_SIZE=500
```

**Для средних нагрузок** (100-1000 IP/s):
```env
BLOCKER_WORKERS=16
BLOCKER_QUEUE_SIZE=1000
```

**Для высоких нагрузок** (1000+ IP/s):
```env
BLOCKER_WORKERS=32
BLOCKER_QUEUE_SIZE=2000
```

### Расчет памяти
- Каждый воркер: ~4-8 KB stack
- Каждая задача в очереди: ~100-200 bytes
- Total overhead: `BLOCKER_WORKERS * 8KB + BLOCKER_QUEUE_SIZE * 200B`
- Пример (16 workers, 1000 queue): ~128KB + 200KB = **~328KB**

## Производительность

### Benchmarks (ориентировочно)
- Обработка 1 IP (без реального nft): ~50-100 μs
- Worker pool overhead: ~5-10 μs per task
- Bounded queue: O(1) отправка/получение
- Graceful shutdown: < 10s для 1000 задач

### Bottleneck
- Реальное выполнение `nft add element`: ~10-50 ms
- Максимальная throughput: `BLOCKER_WORKERS * (1000ms / 20ms) = 16 * 50 = 800 IP/s`
- Рекомендация: увеличить BLOCKER_WORKERS для higher throughput
