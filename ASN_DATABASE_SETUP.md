# Инструкция по получению базы GeoLite2 ASN

## Выбор варианта

| Вариант                    | Регистрация  | Сложность   | Актуальность | Рекомендация        |
| -------------------------- | ------------ | ----------- | ------------ | ------------------- |
| **Вариант 1: MaxMind**     | Требуется    | Средняя     | Максимальная | Для production      |
| **Вариант 2: P3TERX**      | Не требуется | Минимальная | Высокая      | Для быстрого старта |
| **Вариант 3: IP2Location** | Требуется    | Высокая     | Средняя      | Не рекомендуется    |

## Вариант 1: MaxMind GeoLite2 ASN (для production)

### Шаг 1: Регистрация на MaxMind

1. Перейдите на https://www.maxmind.com/en/geolite2/signup
2. Заполните форму регистрации (бесплатно)
3. Подтвердите email

### Шаг 2: Получение лицензионного ключа

1. Войдите в аккаунт https://www.maxmind.com/en/account/login
2. Перейдите в "My License Key"
3. Создайте новый ключ (Generate new license key)
4. Сохраните ключ в безопасном месте

### Шаг 3: Скачивание базы

```bash
# Создайте директорию для базы
mkdir -p observer_conf/data

# Скачайте базу (замените YOUR_LICENSE_KEY на ваш ключ)
cd observer_conf/data

wget -S \
--method HEAD \
--user=YOUR_ACCOUNT_ID \
--password=YOUR_LICENSE_KEY \
'https://download.maxmind.com/geoip/databases/GeoIP2-City-CSV/download?suffix=zip'

# Распакуйте архив
tar -xzf GeoLite2-ASN.tar.gz

# Переместите файл .mmdb в корень data/
mv GeoLite2-ASN_*/GeoLite2-ASN.mmdb .

# Очистите временные файлы
rm -rf GeoLite2-ASN_* GeoLite2-ASN.tar.gz

# Проверьте что файл на месте
ls -lh GeoLite2-ASN.mmdb
```

### Шаг 4: Автоматическое обновление (опционально)

Создайте cron-задачу для автоматического обновления базы раз в неделю:

```bash
# Создайте скрипт обновления
cat > /root/update_asn_db.sh << 'EOF'
#!/bin/bash
LICENSE_KEY="YOUR_LICENSE_KEY"
DATA_DIR="/path/to/observer_conf/data"

cd "$DATA_DIR"
wget "https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-ASN&license_key=$LICENSE_KEY&suffix=tar.gz" -O GeoLite2-ASN.tar.gz
tar -xzf GeoLite2-ASN.tar.gz
mv GeoLite2-ASN_*/GeoLite2-ASN.mmdb .
rm -rf GeoLite2-ASN_* GeoLite2-ASN.tar.gz

# Перезапустите observer для применения обновления
docker restart observer

echo "ASN database updated: $(date)" >> /var/log/asn_update.log
EOF

chmod +x /root/update_asn_db.sh

# Добавьте в crontab (каждую среду в 3:00)
crontab -e
# Добавьте строку:
0 3 * * 3 /root/update_asn_db.sh
```

## Вариант 2: P3TERX GeoLite.mmdb (рекомендуется для быстрого старта) 🚀

**Самый простой способ без регистрации!**

Репозиторий [P3TERX/GeoLite.mmdb](https://github.com/P3TERX/GeoLite.mmdb) предоставляет готовые файлы GeoLite2, которые можно скачать напрямую без регистрации на MaxMind.

### Преимущества:

- ✅ Не требуется регистрация
- ✅ Прямые ссылки на скачивание
- ✅ Автоматические обновления в репозитории
- ✅ Совместимость с форматом MaxMind

### Шаг 1: Скачивание базы

```bash
# Создайте директорию для базы
mkdir -p observer_conf/data
cd observer_conf/data

# Скачайте последнюю версию GeoLite2-ASN
wget https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-ASN.mmdb

# Проверьте размер (должен быть 7-10 MB)
ls -lh GeoLite2-ASN.mmdb
```

### Шаг 2: Автоматическое обновление (опционально)

```bash
# Создайте скрипт обновления
cat > /opt/remnawave-observer/update_asn_db_p3terx.sh <<'EOF'
#!/bin/bash
DATA_DIR="/opt/remnawave-observer/observer_conf/data"
LOG_FILE="$DATA_DIR/asn_update.log"

cd "$DATA_DIR" || exit 1

echo "$(date): Starting ASN update" >> "$LOG_FILE"

wget -O GeoLite2-ASN.mmdb.new "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-ASN.mmdb"

if [ -s GeoLite2-ASN.mmdb.new ]; then
    mv GeoLite2-ASN.mmdb.new GeoLite2-ASN.mmdb
    docker restart observer-remna
    echo "$(date): ASN database updated from P3TERX successfully" >> "$LOG_FILE"
else
    echo "$(date): Failed to download ASN database (file empty or missing)" >> "$LOG_FILE"
    rm -f GeoLite2-ASN.mmdb.new
fi

echo "$(date): Update process finished" >> "$LOG_FILE"
EOF

chmod +x /opt/remnawave-observer/update_asn_db_p3terx.sh

# Добавьте в crontab (каждую среду в 3:00)
crontab -e
# Добавьте строку:
0 3 * * 3 /opt/remnawave-observer/update_asn_db_p3terx.sh
```

### Примечание о частоте обновлений

Репозиторий P3TERX обновляется регулярно, но с задержкой относительно официального MaxMind. Для большинства случаев это не критично, так как изменения в ASN происходят нечасто.

## Вариант 3: IP2Location Lite ASN (альтернатива)

### Шаг 1: Регистрация

1. Перейдите на https://lite.ip2location.com/sign-up
2. Зарегистрируйтесь (бесплатно)

### Шаг 2: Скачивание

1. Войдите в аккаунт
2. Перейдите в раздел "Database Download"
3. Скачайте "IP2Location LITE ASN" в формате BIN

**Примечание:** IP2Location использует другой формат, потребуется адаптер. MaxMind проще в использовании.

## Проверка установки

После скачивания базы проверьте конфигурацию:

```bash
# Проверьте что файл существует
ls -lh observer_conf/data/GeoLite2-ASN.mmdb

# Проверьте размер (должен быть ~7-10 MB)
# Если меньше 1 MB - файл поврежден

# Проверьте конфигурацию в .env
cat observer_conf/.env | grep ASN
```

Должно быть примерно так:

```
DETECT_BY_ASN=true
MAX_ASNS_PER_USER=4
ASN_DATABASE_PATH=/app/data/GeoLite2-ASN.mmdb
```

## Размеры баз данных

- **GeoLite2-ASN.mmdb**: ~7-10 MB
- Обновляется MaxMind: каждый вторник
- Срок жизни лицензии: бесплатно навсегда

## Troubleshooting

### Ошибка: "не удалось открыть ASN базу"

Проверьте:

1. Файл существует: `ls observer_conf/data/GeoLite2-ASN.mmdb`
2. Права на чтение: `chmod 644 observer_conf/data/GeoLite2-ASN.mmdb`
3. Volume смонтирован правильно в docker-compose.yml

### Ошибка: "ASN не найден для IP"

Это нормально для:

- Приватных IP (192.168.x.x, 10.x.x.x)
- Некоторых новых IP-блоков
- Системе автоматически использует fallback на /16 подсеть

### База устарела

MaxMind обновляет базу каждую неделю. Настройте автообновление через cron (см. выше).
