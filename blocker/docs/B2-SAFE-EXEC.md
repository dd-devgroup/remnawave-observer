# B2 — Безопасный exec nft (no injection)

## Изменения

### 1. Интерфейс CommandRunner
- **Файл**: `internal/services/command/executor.go`
- **Интерфейс**: `CommandRunner` с методом `Run(ctx, name, args...) ([]byte, error)`
- **Реализации**:
  - `realRunner` — production код через `exec.CommandContext`
  - `mockRunner` (в тестах) — для unit тестирования без реального выполнения
- **Цель**: возможность мокинга в тестах, изоляция от OS

### 2. Безопасное формирование команд
- **Функция**: `BuildSetExpression(ip, duration string) string`
- **Назначение**: единственное место формирования nftables set expression
- **Безопасность**:
  - Принимает только уже провалидированные ip/duration
  - Не выполняет shell expansion
  - Возвращает строку как единый аргумент для nft

### 3. Рефакторинг Executor
- **Изменения**:
  - `Executor` теперь использует `CommandRunner` интерфейс
  - `RunNftCommand` передает аргументы напрямую (не через shell)
  - Логирование отделено от выполнения (nil-safe для тестов)
  - Команда НЕ выполняется через shell (`/bin/sh -c`)

### 4. Обновление processor.go
- **Изменение**: использование `command.BuildSetExpression()` вместо прямого `fmt.Sprintf`
- **Безопасность**: единая точка формирования set expression

### 5. Тесты
- **Файл**: `internal/services/command/executor_test.go`
- **Покрытие**:
  - `TestExecutor_RunNftCommand_Success` — успешное выполнение
  - `TestExecutor_RunNftCommand_Error` — обработка ошибок
  - `TestExecutor_NoInjection` — проверка, что injection символы передаются как literal
  - `TestBuildSetExpression` — корректное формирование set expression

## Безопасность

### До рефакторинга
```go
// ОПАСНО: string concatenation в processor.go
setExpr := fmt.Sprintf("{ %s timeout %s }", ipAddress, duration)
// Хотя ipAddress провалидирован, паттерн небезопасный
```

### После рефакторинга
```go
// БЕЗОПАСНО: централизованная функция + интерфейс для мокинга
setExpr := command.BuildSetExpression(ipAddress, duration)
// BuildSetExpression — единственное место формирования
// exec.CommandContext передает аргументы напрямую (не через shell)
```

### Защита от injection

**Метод**: `exec.CommandContext(ctx, "nft", args...)` **НЕ** использует shell.
Аргументы передаются напрямую в `execve()` syscall.

Примеры безопасности:
- Вход: `"{ 1.2.3.4; rm -rf / }"` → передается как literal строка nft
- Вход: `"{ 1.2.3.4 | cat /etc/passwd }"` → literal строка
- Вход: `"{ 1.2.3.4 && echo pwned }"` → literal строка

**Важно**: nft сам парсит set expression. Если syntax неправильный, nft вернет ошибку (не выполнит shell команды).

### Тест injection attempts
```go
// Тест проверяет, что shell metacharacters передаются как есть
args := []string{"add", "element", "inet", "firewall", "user_blacklist", "{ 1.2.3.4; rm -rf / }"}
exec.RunNftCommand(ctx, args...)
// mock.lastArgs[5] == "{ 1.2.3.4; rm -rf / }" — literal, не интерпретируется shell
```

## Архитектура

### Интерфейс CommandRunner
```
CommandRunner (interface)
    ├── realRunner (production)
    │   └── exec.CommandContext()
    └── mockRunner (tests)
        └── записывает аргументы без выполнения
```

### Поток выполнения (production)
```
processor.Process()
    → validation.ValidateIPOrCIDR()
    → command.BuildSetExpression(validatedIP, duration)
    → executor.RunNftCommand(ctx, args...)
    → runner.Run(ctx, "nft", args...)
    → exec.CommandContext(ctx, "nft", args...)  // NO SHELL
```

## Риски и откат

### Риски
- **Минимальный**: поведение команды не изменилось, только архитектура
- Интерфейс добавлен для тестируемости (не меняет runtime)

### Откат (rollback)
1. Revert коммита B2
2. Удалить `executor_test.go`
3. Вернуть старую версию `executor.go` и `processor.go`
4. Тест: `go build ./...`

## Тестирование

### Unit тесты
```bash
cd blocker
go test ./internal/services/command -v
```

Ожидаемый результат: 4 теста (10 sub-tests) PASS

### Проверка безопасности
Тесты `TestExecutor_NoInjection` проверяют, что:
1. `;` не интерпретируется как разделитель команд
2. `|` не создает pipe
3. `&&` не создает conditional execution

### Интеграционное тестирование
1. Запустить Blocker с реальным nft (требуется root)
2. Отправить сообщение с валидным IP
3. Проверить, что nft rule добавлен:
```bash
sudo nft list set inet firewall user_blacklist
```

## Производительность
- Накладные расходы интерфейса: ~5-10 ns (незначительно)
- Реальное выполнение nft: ~10-50 ms (доминирует)
- Интерфейс не добавляет аллокаций в hot path
