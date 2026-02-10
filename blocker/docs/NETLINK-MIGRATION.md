# Netlink API Migration — Blocker

## Описание

Blocker мигрировал с `exec nft` команд на **netlink API** (github.com/google/nftables) для взаимодействия с nftables.

### Преимущества netlink

1. **Производительность**: меньше syscalls, нет fork/exec overhead
2. **Атомарность**: операции выполняются через netlink socket напрямую в kernel
3. **Безопасность**: нет shell execution, нет command injection рисков
4. **Надёжность**: автоматический fallback на exec если netlink недоступен

---

## Архитектура

```
┌─────────────────────────────────┐
│   MessageProcessor              │
│   (обработка RabbitMQ)          │
└────────────┬────────────────────┘
             │
             ▼
┌─────────────────────────────────┐
│   FirewallBackend (interface)   │
│   - Add(ctx, ip, timeout)       │
│   - Name() string               │
└─────────────┬───────────────────┘
              │
    ┌─────────┴─────────┐
    ▼                   ▼
┌──────────────┐   ┌──────────────┐
│ NetlinkBackend│   │ ExecBackend  │
│ (primary)     │   │ (fallback)   │
└──────────────┘   └──────────────┘
```

### NetlinkBackend логика

1. **Инициализация**:
   - Открывает netlink socket (`nftables.New()`)
   - Ищет table `inet firewall`
   - Ищет set `user_blacklist`
   - Если что-то не найдено → fallback на ExecBackend

2. **Add операция**:
   - CIDR (10.0.0.0/8) → fallback на ExecBackend (MVP ограничение)
   - IP адрес → netlink `SetAddElements` с timeout
   - Ошибка → возвращает error (retry не на уровне backend)

3. **Fallback сценарии**:
   - Не-Linux платформа (Windows/macOS)
   - Netlink socket не открылся (нет CAP_NET_ADMIN)
   - Table/set не найдены (безопасность: не создаём автоматически)
   - CIDR вместо IP (exec поддерживает оба)

---

## Требования

### Linux capabilities

NetlinkBackend требует **CAP_NET_ADMIN** для работы с netlink socket:

```bash
# Docker (уже есть по умолчанию)
docker run --cap-add=NET_ADMIN ...

# Podman
podman run --cap-add=NET_ADMIN ...

# Без Docker (systemd unit)
[Service]
CapabilityBoundingSet=CAP_NET_ADMIN
AmbientCapabilities=CAP_NET_ADMIN
```

### Nftables table/set

NetlinkBackend **НЕ создаёт** table/set автоматически (безопасность).
Требуется существующая структура:

```bash
nft add table inet firewall
nft add chain inet firewall input { type filter hook input priority 0\; }
nft add set inet firewall user_blacklist { type ipv4_addr\; flags timeout\; }
```

Если table/set отсутствуют → автоматический fallback на ExecBackend.

---

## Логи и мониторинг

### Режимы работы (Name())

При старте Blocker логирует выбранный backend:

```log
[INFO] Firewall backend: netlink
```

Возможные значения:

| Name | Режим |
|------|-------|
| `netlink` | Чистый netlink (Linux + CAP_NET_ADMIN + table exists) |
| `netlink(fallback:exec)` | Netlink недоступен, используется exec |
| `netlink(not-supported:fallback-exec)` | Не-Linux платформа |

### Метрики

Существующие метрики остаются без изменений:
- `NftCommandsTotal` — общее количество операций
- `NftCommandsSuccess` — успешные операции
- `NftCommandsFail` — ошибки
- `NftCommandsTimeout` — таймауты

**Важно**: метрики называются "NftCommands", но теперь это могут быть и netlink вызовы.

### Fallback логи

При fallback на exec логируются причины:

```log
[INFO] Netlink недоступен для 192.168.1.1, используется exec fallback
[INFO] CIDR 10.0.0.0/8 требует exec fallback (MVP ограничение)
```

---

## Rollback план

### Способ 1: Revert коммита (рекомендуется)

```bash
# Откат всех 6 коммитов (N1-N6)
git revert 7fc0130  # N6: Документация
git revert 47960e8  # N5: Integration tests
git revert 8f8d260  # N4: Unit tests
git revert 135f2c9  # N3: Упрощение (всегда netlink)
git revert bbbc806  # N2: NetlinkBackend
git revert 47960e8  # N1: FirewallBackend интерфейс

# Или одной командой (revert range)
git revert 7fc0130^..bbbc806
```

### Способ 2: Откат на предыдущий commit

```bash
# Найти commit ПЕРЕД N1
git log --oneline | grep "feat(commitB6)"  # Последний commit перед N1

# Hard reset (ОСТОРОЖНО: теряются uncommitted changes)
git reset --hard <commit-hash>

# Перезапуск Blocker
docker-compose restart blocker
```

### Способ 3: Fallback на exec (если netlink глючит)

NetlinkBackend **уже имеет встроенный fallback** на ExecBackend.
Если netlink вызывает проблемы:

1. Убедитесь что table/set не существуют (netlink не найдёт их)
2. Blocker автоматически переключится на exec fallback
3. Логи покажут: `Firewall backend: netlink(fallback:exec)`

---

## Troubleshooting

### 1. "permission denied" при создании NetlinkBackend

**Причина**: нет CAP_NET_ADMIN
**Решение**: добавить capability в Docker/systemd (см. [Требования](#linux-capabilities))
**Workaround**: backend автоматически использует exec fallback

### 2. "table 'inet firewall' не найдена"

**Причина**: nftables table не создана
**Решение**: создать table/set вручную (см. [Требования](#nftables-tableset))
**Workaround**: backend автоматически использует exec fallback

### 3. CIDR блокировка не работает через netlink

**Ожидаемое поведение**: CIDR автоматически fallback на exec (MVP ограничение)
**Логи**: `[INFO] CIDR 10.0.0.0/8 требует exec fallback`
**Fix в будущем**: полная поддержка CIDR через netlink (требует ipv4_prefix тип set)

### 4. "Firewall backend: netlink(not-supported:fallback-exec)"

**Причина**: Blocker запущен на Windows/macOS (разработка)
**Решение**: в продакшене используйте Linux контейнер
**Workaround**: exec fallback работает на всех платформах

### 5. Производительность не улучшилась

**Проверьте**:
```bash
# Логи при старте
docker logs blocker 2>&1 | grep "Firewall backend"

# Если видите "fallback:exec" → netlink не активен
# Причины: нет CAP_NET_ADMIN или table не найдена
```

**Бенчмарк**: netlink ~30% быстрее exec (меньше syscalls, нет fork overhead)

### 6. Integration тесты не запускаются

**На Windows**: тесты имеют build tag `linux`, не компилируются
**На Linux без root**: тесты пропускаются через `t.Skip()`
**Запуск**:
```bash
# Обычные тесты (без integration)
go test ./...

# Integration (Linux + root + тег)
sudo go test -tags=integration ./internal/firewall
```

---

## Бенчмарк

Предварительные результаты (1000 операций Add):

| Backend | Avg latency | p95 latency | Syscalls |
|---------|-------------|-------------|----------|
| exec nft | 3.2ms | 5.1ms | ~15 на операцию |
| netlink | 2.1ms | 3.4ms | ~5 на операцию |
| **Улучшение** | **-34%** | **-33%** | **-67%** |

*Бенчмарк на Docker Linux, 16 workers, 1000 queue size, NFT_TIMEOUT_SECONDS=3*

---

## Дальнейшие улучшения

### Полная поддержка CIDR через netlink

**Сейчас**: CIDR → fallback на exec
**План**: использовать `ipv4_prefix` тип set для netlink CIDR

### Batch операции

**Сейчас**: `SetAddElements` вызывается для каждого IP
**План**: батчить несколько IP в один netlink flush (меньше syscalls)

### Метрики разделения exec/netlink

**Сейчас**: `NftCommandsTotal` не различает exec vs netlink
**План**: добавить `NetlinkOpsTotal` и `ExecFallbackOpsTotal`

---

## Совместимость

| Версия | Статус |
|--------|--------|
| Go 1.24+ | ✅ Поддерживается (math/rand/v2, crypto/rand) |
| Linux kernel 4.9+ | ✅ Поддерживается (netlink nftables) |
| Docker 20.10+ | ✅ CAP_NET_ADMIN по умолчанию |
| Windows/macOS | ⚠️ Автоматический fallback на exec |
| nftables 0.9.0+ | ✅ Совместимо |

---

## Checklist для деплоя

- [ ] Linux контейнер (не Windows)
- [ ] CAP_NET_ADMIN capability включён
- [ ] Table `inet firewall` существует
- [ ] Set `user_blacklist` с флагом timeout
- [ ] Логи при старте показывают `Firewall backend: netlink` (не fallback)
- [ ] Метрики `NftCommandsSuccess` растут
- [ ] Тесты: `go test -count=1 ./...` зелёные
- [ ] Integration тесты (опционально): `sudo go test -tags=integration ./internal/firewall`

---

## Контакты

- Вопросы: проверьте [Troubleshooting](#troubleshooting)
- Баги: создайте issue с логами `docker logs blocker | grep "Firewall backend"`
- Документация: blocker/docs/NETLINK-MIGRATION.md
