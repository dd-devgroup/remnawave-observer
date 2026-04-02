-- KEYS[1]: user ASN IP set key (for example, user_asn_ips:12345:AS25159)
-- ARGV[1]: IP to add
-- ARGV[2]: TTL in seconds

local isNewIP = redis.call('SADD', KEYS[1], ARGV[1])
redis.call('EXPIRE', KEYS[1], ARGV[2])
local currentCount = redis.call('SCARD', KEYS[1])

return {isNewIP, currentCount}
