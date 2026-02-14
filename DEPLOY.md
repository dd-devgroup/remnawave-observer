# Deployment Guide - Integrated Observer Service

## Что изменилось

### До (2 сервиса):
```yaml
services:
  observer-updater:  # Отдельный сервис для обновления данных
    build: Dockerfile.updater
  observer:          # Основной сервис
    build: Dockerfile
```

### После (1 сервис):
```yaml
services:
  observer:          # Всё в одном: observer + updater
    build: Dockerfile
    environment:
      - UPDATER_ENABLED=true  # Включить фоновые обновления
```

## Преимущества

✅ **Один образ вместо двух** - проще деплой и версионирование
✅ **Фикс прав доступа** - больше НЕ нужно `sudo chown -R 1000:1000 ./data`
✅ **Меньше ресурсов** - один процесс вместо двух
✅ **Проще управление** - одна команда вместо двух

## Быстрый старт

### 1. Сборка образа

```bash
cd observer
docker build -t remnawave/observer:latest .
```

Или через docker-compose:

```bash
cd observer_conf
docker-compose build
```

### 2. Запуск

```bash
cd observer_conf
docker-compose up -d
```

### 3. Проверка логов

```bash
# Основной сервис
docker logs -f observer

# Вы должны увидеть:
# [Updater] ✅ ASN updater initialized
# [Updater] ✅ CAIDA AS2Org updater initialized
# [Updater] ✅ GeoLite updater initialized
# [Observer] Observer Service server started on port 9000
```

## Проблема с правами (РЕШЕНА)

### ❌ Старая проблема:
```bash
# Раньше нужно было делать вручную:
sudo chown -R 1000:1000 ./data
sudo chmod -R 755 ./data
```

### ✅ Новое решение:
**Больше ничего делать НЕ нужно!**

Dockerfile создаёт пользователя `observer` (UID/GID 1000) и все файлы автоматически создаются с правильными правами:

```dockerfile
# В Dockerfile:
RUN addgroup -g 1000 observer && \
    adduser -D -u 1000 -G observer observer

RUN mkdir -p /app/data && \
    chown -R observer:observer /app

USER observer  # Всё работает от пользователя 1000
```

## Конфигурация

### Переменные окружения (.env)

```bash
# Управление updater
UPDATER_ENABLED=true              # Включить фоновые обновления (default: true)

# ASN updates
IPTOASN_UPDATE_INTERVAL_HOURS=24  # Обновлять iptoasn каждые 24ч

# CAIDA updates
CAIDA_ENABLED=true                 # Включить CAIDA AS2Org
CAIDA_REFRESH_HOURS=168           # Обновлять раз в неделю

# GeoLite updates
GEOLITE_UPDATE_INTERVAL_HOURS=168 # Обновлять раз в неделю
```

### Отключение updater (если нужно)

Если хотите отключить фоновые обновления (например, данные обновляются извне):

```bash
# В .env:
UPDATER_ENABLED=false
```

## Структура файлов

```
./data/                                # Shared volume (auto-created with correct permissions)
├── ip2asn-v4.tsv                     # ASN database (updated by integrated updater)
├── GeoLite2-ASN.mmdb                 # GeoLite ASN MMDB
├── GeoLite2-City.mmdb                # GeoLite City MMDB
├── as2org.txt                        # CAIDA AS2Org data
├── unknown_providers.json            # Auto-learning data
└── providers.learned.yaml            # Learned providers overlay

./config/                              # Read-only configs
├── agglomerations.yaml
└── providers.yaml
```

## Миграция с двух сервисов

### 1. Остановите старые контейнеры

```bash
docker-compose down
```

### 2. (Опционально) Бекап данных

```bash
tar -czf data-backup-$(date +%Y%m%d).tar.gz ./data
```

### 3. Обновите docker-compose.yml

```bash
cp docker-compose.example.yml docker-compose.yml
# Отредактируйте под свои нужды
```

### 4. Пересоберите и запустите

```bash
docker-compose build
docker-compose up -d
```

### 5. Проверьте

```bash
docker logs observer | grep Updater
# Должны увидеть сообщения об инициализации updater'ов

ls -la ./data
# Файлы должны принадлежать 1000:1000
```

## Troubleshooting

### Проблема: "permission denied" при записи в ./data

**Решение:**
Если папка ./data уже существует с другими правами:

```bash
# Удалите старую папку или измените владельца
sudo chown -R 1000:1000 ./data

# Или просто удалите и контейнер создаст заново
rm -rf ./data
docker-compose up -d  # Создаст ./data с правильными правами
```

### Проблема: Updater не запускается

**Проверка:**
```bash
docker exec observer env | grep UPDATER
# Должно быть: UPDATER_ENABLED=true

docker logs observer | grep -i updater
# Должны быть сообщения: [Updater] ✅ ...
```

### Проблема: Файлы не обновляются

**Проверка интервалов:**
```bash
# В .env проверьте:
IPTOASN_UPDATE_INTERVAL_HOURS=24
CAIDA_REFRESH_HOURS=168
GEOLITE_UPDATE_INTERVAL_HOURS=168

# Перезапустите после изменения .env:
docker-compose restart observer
```

## Мониторинг

### Проверка работы updater

```bash
# Логи updater
docker logs observer 2>&1 | grep Updater

# Проверка последних обновлений файлов
docker exec observer ls -lht /app/data | head -10

# Статистика ASN базы
docker logs observer 2>&1 | grep "ASN lookup loaded"
# Должно быть: ASN lookup loaded from file (records: 123456)
```

## Production Ready

Этот setup готов для production:

- ✅ Non-root user (security best practice)
- ✅ Правильные права доступа (1000:1000)
- ✅ Health checks (опционально, раскомментируйте в docker-compose)
- ✅ Log rotation (json-file driver с ротацией)
- ✅ Graceful shutdown (SIGTERM handling)
- ✅ Auto-restart (restart: unless-stopped)

## Версионирование

```bash
# Тегирование образа
docker tag remnawave/observer:latest remnawave/observer:v2.1.0

# Push в registry
docker push quay.io/fxfuren/remnawave-observer:v2.1.0
docker push quay.io/fxfuren/remnawave-observer:latest
```

---

**Вопросы?** Проверьте логи: `docker logs observer`
