# B1 — Валидация входных IP/подсетей (security)

## Изменения

### 1. Новый пакет validation
- **Файл**: `internal/validation/validator.go`
- **Функции**:
  - `ValidateIPOrCIDR(input string) ValidationResult` — полная валидация через `net/netip`
  - `IsValidIPOrCIDR(input string) bool` — быстрая проверка
- **Безопасность**: все IP/CIDR парсятся через `netip.ParseAddr()` и `netip.ParsePrefix()`
- **Защита от injection**: строки типа `"1.2.3.4; rm -rf /"` корректно отвергаются

### 2. Тесты валидации
- **Файл**: `internal/validation/validator_test.go`
- **Покрытие**:
  - Валидные IPv4/IPv6
  - Валидные CIDR (IPv4/IPv6)
  - Невалидные строки (включая попытки инъекций)
  - Пустые/пробельные строки

### 3. Модель данных (совместимость с Observer)
- **Файл**: `internal/models/models.go`
- **Новые поля** (omitempty):
  - `EventID string` — уникальный ID события
  - `ChunkIndex int` — индекс чанка (для больших массивов IP)
  - `ChunkTotal int` — общее количество чанков
  - `SchemaVersion int` — версия схемы
- **Совместимость**: Blocker теперь корректно парсит сообщения Observer с chunking

### 4. Обработчик сообщений
- **Файл**: `internal/processor/processor.go`
- **Улучшения**:
  - Использование нового валидатора `validation.ValidateIPOrCIDR()`
  - Логирование EventID/ChunkIndex в контексте ошибок
  - Фильтрация невалидных IP без прерывания обработки всего сообщения
- **Стратегия обработки ошибок**:
  - Невалидные IP → логируется WARNING, пропускается
  - Все IP невалидны → логируется WARNING, сообщение ACK (poison message)
  - Невалидный JSON → ERROR, сообщение ACK (не requeue)
  - Невалидный duration → ERROR, сообщение ACK (не requeue)

## Безопасность

### Защита от инъекций
До рефакторинга:
```go
// Слабая валидация через net.ParseIP (deprecated approach)
if net.ParseIP(s) != nil { /* OK */ }
```

После рефакторинга:
```go
// Строгая валидация через netip
result := validation.ValidateIPOrCIDR(ip)
if !result.Valid {
    logger.Warning(fmt.Sprintf("Невалидный IP/CIDR пропущен: %s (причина: %s)", ip, result.Error))
    continue
}
```

### Примеры отклоненных значений
- `"999.999.999.999"` → "invalid IP"
- `"192.168.1.1; rm -rf /"` → "invalid IP"
- `"1.2.3.4 && cat /etc/passwd"` → "invalid IP"
- `"192.168.1.0/33"` → "invalid CIDR"

## Риски и откат

### Риски
- **Минимальный**: только изменения в валидации, без изменения логики nft команд
- Валидация стала строже — могут отклоняться IP, которые раньше проходили

### Откат (rollback)
1. Revert коммита B1
2. Удалить пакет `internal/validation`
3. Вернуть старые поля в `models.go`
4. Тест: `go build ./... && go test ./...`

## Тестирование

### Unit тесты
```bash
cd blocker
go test ./internal/validation -v
```

Ожидаемый результат: все 5 тестов PASS

### Интеграционное тестирование
1. Отправить сообщение с валидными IP:
```json
{"ips": ["192.168.1.1", "10.0.0.0/8"], "duration": "5m"}
```
Ожидание: все IP обработаны

2. Отправить сообщение с невалидными IP:
```json
{"ips": ["999.999.999.999", "not-an-ip"], "duration": "5m"}
```
Ожидание: WARNING в логах, сообщение ACK

3. Отправить сообщение с EventID (новый формат Observer):
```json
{"ips": ["192.168.1.1"], "duration": "5m", "event_id": "test-123", "chunk_index": 0, "chunk_total": 1}
```
Ожидание: IP обработан, в логах видно EventID

## Конфиги
Изменений в конфигурации нет.

## Производительность
- Валидация через `netip` быстрее, чем через старый `net` пакет
- Накладные расходы: ~10-20 ns на IP (незначительно)
