# B4: Таймауты на nft операции + защита от зависаний

## Проблема
Без таймаутов nft операции могут зависнуть бесконечно при проблемах с nftables/kernel.

## Решение
- Конфиг `NFT_TIMEOUT_SECONDS` (default: 3s)
- Каждая nft операция выполняется с `context.WithTimeout`
- Child context от pool context (учитывает shutdown)
- Логирование timeout ошибок отдельно от других

## Изменения
- `config.go`: +NftTimeout, getEnvDuration()
- `processor.go`: context.WithTimeout для каждой nft операции
- `processor_timeout_test.go`: 2 теста (timeout + cancellation)

## Поведение при ошибках
- **Timeout (DeadlineExceeded)**: ERROR лог с указанием времени
- **Cancellation**: WARNING (shutdown в процессе)
- **Другие ошибки**: ERROR лог

Сообщение обрабатывается до конца (wg.Wait), все успешные IP применяются.

## Тесты
```bash
go test ./internal/processor -v -run TestMessageProcessor_Nft
```
- TestMessageProcessor_NftTimeout: операция с delay 2s прерывается по timeout 100ms
- TestMessageProcessor_NftContextCancellation: pool shutdown отменяет контекст

## Риски
- **Низкий**: только добавление timeout, не меняет логику ack/nack
- **Rollback**: удалить context.WithTimeout, вернуть прямой вызов

## Конфигурация
```env
NFT_TIMEOUT_SECONDS=3  # default
```
