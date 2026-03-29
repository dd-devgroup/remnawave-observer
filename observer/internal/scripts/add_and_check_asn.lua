-- KEYS[1]: user ASN set key (for example, user_asns:12345)
-- ARGV[1]: ASN to add (for example, "AS25159")
-- ARGV[2]: TTL for the ASN marker in seconds

-- Add ASN to the user set.
local isNewASN = redis.call('SADD', KEYS[1], ARGV[1])

-- Extract user identifier from "user_asns:{user}".
local userEmail = string.sub(KEYS[1], 11)
local asnTtlKey = 'asn_ttl:' .. userEmail .. ':' .. ARGV[1]
redis.call('SETEX', asnTtlKey, ARGV[2], '1')

-- Read every ASN currently associated with the user.
local allASNs = redis.call('SMEMBERS', KEYS[1])

local activeASNs = {}
local deadASNs = {}

for i, asn in ipairs(allASNs) do
    local asnKey = 'asn_ttl:' .. userEmail .. ':' .. asn
    local ttl = redis.call('TTL', asnKey)

    if ttl > 0 then
        table.insert(activeASNs, asn)
    else
        table.insert(deadASNs, asn)
    end
end

if #deadASNs > 0 then
    for i, asn in ipairs(deadASNs) do
        redis.call('SREM', KEYS[1], asn)
    end
end

if #activeASNs > 0 then
    local maxTTL = 0
    for i, asn in ipairs(activeASNs) do
        local asnKey = 'asn_ttl:' .. userEmail .. ':' .. asn
        local ttl = redis.call('TTL', asnKey)
        if ttl > maxTTL then
            maxTTL = ttl
        end
    end

    if maxTTL > 0 then
        redis.call('EXPIRE', KEYS[1], maxTTL + 60)
    else
        redis.call('EXPIRE', KEYS[1], ARGV[2])
    end
else
    redis.call('DEL', KEYS[1])
end

local currentASNCount = #activeASNs
return {0, currentASNCount, isNewASN, activeASNs}
