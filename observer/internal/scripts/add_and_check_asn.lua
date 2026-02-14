-- KEYS[1]: ключ множества ASN пользователя (например, user_asns:tg15_32732832)
-- KEYS[2]: ключ кулдауна алертов для пользователя (например, alert_sent:tg15_32732832)
-- ARGV[1]: ASN для добавления (например, "AS25159")
-- ARGV[2]: TTL для ASN в секундах
-- ARGV[3]: Лимит ASN для пользователя
-- ARGV[4]: TTL для кулдауна алертов в секундах

-- Добавляем новый ASN в множество
local isNewASN = redis.call('SADD', KEYS[1], ARGV[1])

-- Извлекаем email из ключа KEYS[1] (user_asns: -> 10 символов)
local userEmail = string.sub(KEYS[1], 11)
-- Формируем ключ для TTL
local asnTtlKey = 'asn_ttl:' .. userEmail .. ':' .. ARGV[1]
redis.call('SETEX', asnTtlKey, ARGV[2], '1')

-- НЕ обновляем TTL всего множества! Это ключевое отличие от subnet скрипта
-- redis.call('EXPIRE', KEYS[1], ARGV[2]) -- УДАЛЕНО

-- Получаем все ASN из множества
local allASNs = redis.call('SMEMBERS', KEYS[1])

-- Фильтруем "мертвые" ASN (с истекшим TTL)
local activeASNs = {}
local deadASNs = {}

for i, asn in ipairs(allASNs) do
    local asnKey = 'asn_ttl:' .. userEmail .. ':' .. asn
    local ttl = redis.call('TTL', asnKey)

    -- ttl > 0 означает что ключ существует и не истек
    -- ttl == -2 означает что ключ не существует
    -- ttl == -1 означает что ключ существует но без TTL (не должно быть в нашем случае)
    if ttl > 0 then
        table.insert(activeASNs, asn)
    else
        table.insert(deadASNs, asn)
    end
end

-- Удаляем "мертвые" ASN из множества
if #deadASNs > 0 then
    for i, asn in ipairs(deadASNs) do
        redis.call('SREM', KEYS[1], asn)
    end
end

-- Обновляем TTL множества на основе максимального TTL активных ASN
-- Это гарантирует, что множество само не истечет пока есть активные ASN
if #activeASNs > 0 then
    -- Находим максимальный TTL среди всех активных ASN
    local maxTTL = 0
    for i, asn in ipairs(activeASNs) do
        local asnKey = 'asn_ttl:' .. userEmail .. ':' .. asn
        local ttl = redis.call('TTL', asnKey)
        if ttl > maxTTL then
            maxTTL = ttl
        end
    end

    -- Устанавливаем TTL множества равным максимальному TTL + небольшой запас
    -- Это гарантирует что множество не истечет пока есть хоть один активный ASN
    if maxTTL > 0 then
        redis.call('EXPIRE', KEYS[1], maxTTL + 60)
    else
        -- Fallback если не смогли получить TTL
        redis.call('EXPIRE', KEYS[1], ARGV[2])
    end
else
    -- Если нет активных ASN - удаляем само множество
    redis.call('DEL', KEYS[1])
end

local currentASNCount = #activeASNs
local asnLimit = tonumber(ARGV[3])

if currentASNCount > asnLimit then
    local alertSent = redis.call('EXISTS', KEYS[2])
    if alertSent == 0 then
        redis.call('SETEX', KEYS[2], ARGV[4], '1')
        -- Возвращаем только активные ASN
        return {1, activeASNs}
    else
        return {2, currentASNCount}
    end
end

-- For status=0 return active ASN list as well (used by scoring on new ASN events)
return {0, currentASNCount, isNewASN, activeASNs}
