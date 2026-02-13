package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"observer_service/internal/metrics"
	"observer_service/internal/models"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Скрипт для атомарной очистки всех ключей ASN пользователя.
const clearUserASNsScript = `
local asns = redis.call('SMEMBERS', KEYS[1])
if #asns == 0 then
    return redis.call('DEL', KEYS[1])
end
local keysToDelete = { KEYS[1] }
local prefix = ARGV[1]
for i, asn in ipairs(asns) do
    table.insert(keysToDelete, prefix .. ':' .. asn)
end
return redis.call('DEL', unpack(keysToDelete))
`

// Storage defines the interface for the hot-window store (real-time ASN tracking).
type Storage interface {
	CheckAndAddASN(ctx context.Context, email, asn string, limit int, ttl, cooldown time.Duration) (*models.CheckResult, error)
	AddIPToASNMapping(ctx context.Context, email, asn, ip string, ttl time.Duration) error
	SetASNOrgName(ctx context.Context, asn, orgName string, ttl time.Duration) error
	GetIPsForUserASN(ctx context.Context, email, asn string) ([]string, error)
	GetASNOrgName(ctx context.Context, asn string) (string, error)
	GetUserActiveASNs(ctx context.Context, userEmail string) (map[string]*models.ASNInfo, error)
	HasAlertCooldown(ctx context.Context, userEmail string) (bool, error)
	ClearUserASNData(ctx context.Context, email string) (int, error)
	Ping(ctx context.Context) error
	Close() error
}

// RedisStore реализует Storage с использованием Redis.
type RedisStore struct {
	client               *redis.Client
	addCheckASNScriptSHA string
	clearASNsScriptSHA   string
	scanMaxKeys          int
	scanCount            int
	scanTimeBudget       time.Duration
}

// NewRedisStore создает новый экземпляр RedisStore.
func NewRedisStore(ctx context.Context, redisURL string, scriptPaths ...string) (*RedisStore, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("ошибка парсинга Redis URL: %w", err)
	}
	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ошибка подключения к Redis: %w", err)
	}
	// Загрузка скрипта проверки и добавления ASN из файла
	addCheckASNScript, err := os.ReadFile("internal/scripts/add_and_check_asn.lua")
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения Lua-скрипта 'add_and_check_asn.lua': %w", err)
	}
	addCheckASNScriptSHA, err := client.ScriptLoad(ctx, string(addCheckASNScript)).Result()
	if err != nil {
		return nil, fmt.Errorf("ошибка загрузки Lua-скрипта (add/check asn) в Redis: %w", err)
	}
	// Загрузка скрипта атомарной очистки ASN из константы
	clearASNsScriptSHA, err := client.ScriptLoad(ctx, clearUserASNsScript).Result()
	if err != nil {
		return nil, fmt.Errorf("ошибка загрузки Lua-скрипта (clear asn) в Redis: %w", err)
	}
	log.Println("Successfully connected to Redis and loaded Lua scripts")
	return &RedisStore{
		client:               client,
		addCheckASNScriptSHA: addCheckASNScriptSHA,
		clearASNsScriptSHA:   clearASNsScriptSHA,
		scanMaxKeys:          10000,
		scanCount:            100,
		scanTimeBudget:       30 * time.Second,
	}, nil
}

// SetScanMaxKeys задаёт верхнюю границу количества ключей, сканируемых за один вызов SCAN.
func (s *RedisStore) SetScanMaxKeys(n int) {
	if n > 0 {
		s.scanMaxKeys = n
	}
}

// SetScanCount задаёт hint COUNT для Redis SCAN команд.
func (s *RedisStore) SetScanCount(n int) {
	if n > 0 {
		s.scanCount = n
	}
}

// SetScanTimeBudget задаёт верхнюю границу по времени одной SCAN операции.
func (s *RedisStore) SetScanTimeBudget(d time.Duration) {
	if d > 0 {
		s.scanTimeBudget = d
	}
}

// CheckAndAddASN выполняет Lua-скрипт для атомарной проверки и добавления ASN с фильтрацией "мертвых" ASN.
func (s *RedisStore) CheckAndAddASN(ctx context.Context, email, asn string, limit int, ttl, cooldown time.Duration) (*models.CheckResult, error) {
	userASNsSetKey := fmt.Sprintf("user_asns:%s", email)
	alertSentKey := fmt.Sprintf("alert_sent:%s", email)
	args := []interface{}{
		asn,
		int(ttl.Seconds()),
		limit,
		int(cooldown.Seconds()),
	}
	result, err := s.client.EvalSha(ctx, s.addCheckASNScriptSHA, []string{userASNsSetKey, alertSentKey}, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения Lua-скрипта (asn) для %s: %w", email, err)
	}
	return parseCheckResult(result, email)
}

func parseCheckResult(result interface{}, identifier string) (*models.CheckResult, error) {
	resSlice, ok := result.([]interface{})
	if !ok || len(resSlice) < 1 {
		return nil, fmt.Errorf("неожиданный результат от Lua-скрипта для %s", identifier)
	}
	statusCode, _ := resSlice[0].(int64)
	checkResult := &models.CheckResult{StatusCode: statusCode}
	switch statusCode {
	case 0: // OK
		checkResult.CurrentCount, _ = resSlice[1].(int64)
		isNew, _ := resSlice[2].(int64)
		checkResult.IsNew = isNew == 1
	case 1: // Limit exceeded, block
		itemsInterfaces, _ := resSlice[1].([]interface{})
		for _, itemInt := range itemsInterfaces {
			if itemStr, ok := itemInt.(string); ok {
				checkResult.AllUserItems = append(checkResult.AllUserItems, itemStr)
			}
		}
		checkResult.CurrentCount = int64(len(checkResult.AllUserItems))
	case 2: // Limit exceeded, on cooldown
		checkResult.CurrentCount, _ = resSlice[1].(int64)
	}
	return checkResult, nil
}

// GetAllUserEmails сканирует ключи Redis для получения всех username (email) пользователей.
// Операция ограничена scanMaxKeys (кол-во ключей), scanTimeBudget (время) и дедлайном ctx.
// При достижении любого из лимитов возвращает частичный результат и логирует предупреждение.
func (s *RedisStore) GetAllUserEmails(ctx context.Context) ([]string, error) {
	scanCtx, cancel := context.WithTimeout(ctx, s.scanTimeBudget)
	defer cancel()

	var cursor uint64
	emailSet := make(map[string]struct{})
	deadline := time.Now().Add(s.scanTimeBudget)

	pattern := "user_asns:*"
	scanned := 0
	for {
		if time.Now().After(deadline) {
			log.Printf("GetAllUserEmails: time budget %v reached, partial result", s.scanTimeBudget)
			metrics.ScanPartialRunsCount.Add(1)
			break
		}
		var keys []string
		var err error
		keys, cursor, err = s.client.Scan(scanCtx, cursor, pattern, int64(s.scanCount)).Result()
		if err != nil {
			if scanCtx.Err() != nil {
				log.Printf("GetAllUserEmails: context cancelled (%v), partial result", scanCtx.Err())
				metrics.ScanPartialRunsCount.Add(1)
				break
			}
			return nil, fmt.Errorf("ошибка при сканировании ключей по паттерну %s: %w", pattern, err)
		}
		scanned += len(keys)
		for _, key := range keys {
			parts := strings.SplitN(key, ":", 2)
			if len(parts) == 2 {
				emailSet[parts[1]] = struct{}{}
			}
		}
		if cursor == 0 || scanned >= s.scanMaxKeys {
			if scanned >= s.scanMaxKeys {
				log.Printf("GetAllUserEmails: scan limit %d keys reached", s.scanMaxKeys)
			}
			break
		}
	}

	emails := make([]string, 0, len(emailSet))
	for email := range emailSet {
		emails = append(emails, email)
	}
	return emails, nil
}

// HasAlertCooldown проверяет наличие ключа кулдауна для пользователя.
func (s *RedisStore) HasAlertCooldown(ctx context.Context, userEmail string) (bool, error) {
	alertCooldownKey := fmt.Sprintf("alert_sent:%s", userEmail)
	res, err := s.client.Exists(ctx, alertCooldownKey).Result()
	if err != nil {
		return false, err
	}
	return res > 0, nil
}

// Ping проверяет соединение с Redis.
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Close закрывает соединение с Redis.
func (s *RedisStore) Close() error {
	return s.client.Close()
}

// --- МЕТОДЫ ДЛЯ РАБОТЫ С ASN ---

// AddIPToASNMapping добавляет связь ASN -> IP для пользователя
// Это позволяет отслеживать какие IP принадлежат каждому ASN пользователя
func (s *RedisStore) AddIPToASNMapping(ctx context.Context, email, asn, ip string, ttl time.Duration) error {
	key := fmt.Sprintf("user_asn_ips:%s:%s", email, asn)
	pipe := s.client.Pipeline()
	pipe.SAdd(ctx, key, ip)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// GetIPsForUserASN возвращает все IP пользователя для данного ASN
func (s *RedisStore) GetIPsForUserASN(ctx context.Context, email, asn string) ([]string, error) {
	key := fmt.Sprintf("user_asn_ips:%s:%s", email, asn)
	return s.client.SMembers(ctx, key).Result()
}

// GetAllIPsForUser возвращает все IP пользователя из всех ASN.
// Ограничено scanMaxKeys, scanTimeBudget и дедлайном ctx.
func (s *RedisStore) GetAllIPsForUser(ctx context.Context, email string) ([]string, error) {
	scanCtx, cancel := context.WithTimeout(ctx, s.scanTimeBudget)
	defer cancel()

	pattern := fmt.Sprintf("user_asn_ips:%s:*", email)
	uniqueIPs := make(map[string]struct{})
	deadline := time.Now().Add(s.scanTimeBudget)

	scanned := 0
	iter := s.client.Scan(scanCtx, 0, pattern, int64(s.scanCount)).Iterator()
	for iter.Next(scanCtx) {
		if time.Now().After(deadline) {
			log.Printf("GetAllIPsForUser(%s): time budget %v reached, partial result", email, s.scanTimeBudget)
			metrics.ScanPartialRunsCount.Add(1)
			break
		}
		scanned++
		if scanned > s.scanMaxKeys {
			log.Printf("GetAllIPsForUser(%s): scan limit %d keys reached", email, s.scanMaxKeys)
			break
		}
		key := iter.Val()
		ips, err := s.client.SMembers(scanCtx, key).Result()
		if err != nil {
			continue
		}
		for _, ip := range ips {
			uniqueIPs[ip] = struct{}{}
		}
	}

	if err := iter.Err(); err != nil && scanCtx.Err() == nil {
		return nil, err
	}

	allIPs := make([]string, 0, len(uniqueIPs))
	for ip := range uniqueIPs {
		allIPs = append(allIPs, ip)
	}
	return allIPs, nil
}

// ClearUserASNData очищает все данные ASN и связанные IP для пользователя
func (s *RedisStore) ClearUserASNData(ctx context.Context, email string) (int, error) {
	// Очищаем ASN используя специальный скрипт
	userASNsKey := fmt.Sprintf("user_asns:%s", email)
	asnTtlPrefix := fmt.Sprintf("asn_ttl:%s", email)
	deleted, err := s.client.EvalSha(ctx, s.clearASNsScriptSHA, []string{userASNsKey}, asnTtlPrefix).Int64()
	if err != nil {
		if err == redis.Nil {
			deleted = 0
		} else {
			return 0, fmt.Errorf("ошибка выполнения Lua-скрипта (clear asn) для %s: %w", email, err)
		}
	}

	// Затем очищаем mapping ASN -> IPs
	pattern := fmt.Sprintf("user_asn_ips:%s:*", email)
	iter := s.client.Scan(ctx, 0, pattern, 0).Iterator()
	var keysToDelete []string

	for iter.Next(ctx) {
		keysToDelete = append(keysToDelete, iter.Val())
	}

	if err := iter.Err(); err != nil {
		return int(deleted), err
	}

	if len(keysToDelete) > 0 {
		delCount, err := s.client.Del(ctx, keysToDelete...).Result()
		if err != nil {
			return int(deleted), err
		}
		deleted += delCount
	}

	return int(deleted), nil
}

// SetASNOrgName кеширует название организации для ASN
func (s *RedisStore) SetASNOrgName(ctx context.Context, asn, orgName string, ttl time.Duration) error {
	if orgName == "" {
		return nil // Не кешируем пустые названия
	}
	key := fmt.Sprintf("asn_org:%s", asn)
	return s.client.Set(ctx, key, orgName, ttl).Err()
}

// GetASNOrgName получает закешированное название организации для ASN
func (s *RedisStore) GetASNOrgName(ctx context.Context, asn string) (string, error) {
	key := fmt.Sprintf("asn_org:%s", asn)
	result, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil // Нет в кеше - не ошибка
	}
	return result, err
}

// GetUserActiveASNs возвращает все активные ASN пользователя с их TTL и IP-адресами
func (s *RedisStore) GetUserActiveASNs(ctx context.Context, userEmail string) (map[string]*models.ASNInfo, error) {
	// ASN хранятся в отдельном множестве: user_asns:{email}
	key := fmt.Sprintf("user_asns:%s", userEmail)
	asns, err := s.client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	result := make(map[string]*models.ASNInfo)
	for _, asn := range asns {
		// Получаем TTL для каждого ASN
		asnKey := fmt.Sprintf("asn_ttl:%s:%s", userEmail, asn)
		ttl, err := s.client.TTL(ctx, asnKey).Result()
		if err != nil || ttl <= 0 {
			continue
		}

		// Получаем IP-адреса для этого ASN
		ipsKey := fmt.Sprintf("user_asn_ips:%s:%s", userEmail, asn)
		ips, err := s.client.SMembers(ctx, ipsKey).Result()
		if err != nil {
			ips = []string{}
		}

		// Получаем название организации из кеша
		org, _ := s.GetASNOrgName(ctx, asn)
		if org == "" {
			org = "Unknown"
		}

		result[asn] = &models.ASNInfo{
			ASN:          asn,
			Organization: org,
			TTLSeconds:   int(ttl.Seconds()),
			IPs:          ips,
			IPCount:      len(ips),
		}
	}

	return result, nil
}

// GetClient возвращает Redis клиент для использования в других сервисах
func (s *RedisStore) GetClient() *redis.Client {
	return s.client
}

// --- Remnawave Enforcement: Disable Schedule Methods (MIG-4) ---

// GetUserUUIDCache получает кэшированный UUID пользователя по internal ID.
func (s *RedisStore) GetUserUUIDCache(ctx context.Context, internalID int64) (string, bool) {
	key := fmt.Sprintf("rw:uid2uuid:%d", internalID)
	val, err := s.client.Get(ctx, key).Result()
	if err != nil || val == "" {
		return "", false
	}
	return val, true
}

// SetUserUUIDCache сохраняет UUID в кэш с TTL.
func (s *RedisStore) SetUserUUIDCache(ctx context.Context, internalID int64, uuid string, ttl time.Duration) error {
	key := fmt.Sprintf("rw:uid2uuid:%d", internalID)
	return s.client.Set(ctx, key, uuid, ttl).Err()
}

// DisableRecord хранит информацию о disable операции.
type DisableRecord struct {
	InternalID int64  `json:"internal_id"`
	UUID       string `json:"uuid"`
	UntilUnix  int64  `json:"until_unix"`
	Reason     string `json:"reason"`
	Score      int    `json:"score"`
}

// ScheduleDisable планирует автоматический enable на заданное время.
// Сохраняет запись в rw:disable:{id} и добавляет в ZSET rw:reenable:zset.
func (s *RedisStore) ScheduleDisable(ctx context.Context, internalID int64, uuid string, untilUnix int64, reason string, score int) error {
	recordKey := fmt.Sprintf("rw:disable:%d", internalID)
	zsetKey := "rw:reenable:zset"

	// Сохраняем детали disable (JSON в Redis string)
	record := DisableRecord{
		InternalID: internalID,
		UUID:       uuid,
		UntilUnix:  untilUnix,
		Reason:     reason,
		Score:      score,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal disable record: %w", err)
	}

	// TTL: until + 24h (чтобы можно было посмотреть историю)
	ttl := time.Until(time.Unix(untilUnix, 0)) + 24*time.Hour
	if ttl < 0 {
		ttl = 24 * time.Hour // если untilUnix уже прошёл, минимум 24h
	}

	if err := s.client.Set(ctx, recordKey, data, ttl).Err(); err != nil {
		return fmt.Errorf("save disable record: %w", err)
	}

	// Добавляем в ZSET для планировщика (score = untilUnix)
	if err := s.client.ZAdd(ctx, zsetKey, redis.Z{
		Score:  float64(untilUnix),
		Member: fmt.Sprintf("%d", internalID),
	}).Err(); err != nil {
		return fmt.Errorf("add to reenable zset: %w", err)
	}

	return nil
}

// PopDueDisables атомарно извлекает ID пользователей у которых наступило время enable.
// Использует Lua для ZRANGEBYSCORE + ZREM.
func (s *RedisStore) PopDueDisables(ctx context.Context, nowUnix int64, limit int) ([]int64, error) {
	zsetKey := "rw:reenable:zset"

	// Lua script: атомарный ZRANGEBYSCORE + ZREM
	script := `
		local zset = KEYS[1]
		local max_score = tonumber(ARGV[1])
		local limit = tonumber(ARGV[2])
		local members = redis.call('ZRANGEBYSCORE', zset, '-inf', max_score, 'LIMIT', 0, limit)
		if #members > 0 then
			redis.call('ZREM', zset, unpack(members))
		end
		return members
	`

	result, err := s.client.Eval(ctx, script, []string{zsetKey}, nowUnix, limit).Result()
	if err != nil {
		return nil, fmt.Errorf("lua pop due disables: %w", err)
	}

	// Парсим результат (массив строк) в []int64
	members, ok := result.([]interface{})
	if !ok {
		return []int64{}, nil
	}

	ids := make([]int64, 0, len(members))
	for _, m := range members {
		idStr, ok := m.(string)
		if !ok {
			continue
		}
		var id int64
		if _, err := fmt.Sscanf(idStr, "%d", &id); err == nil {
			ids = append(ids, id)
		}
	}

	return ids, nil
}

// GetDisableRecord получает информацию о disable по internal ID.
func (s *RedisStore) GetDisableRecord(ctx context.Context, internalID int64) (*DisableRecord, bool) {
	key := fmt.Sprintf("rw:disable:%d", internalID)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}

	var record DisableRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, false
	}

	return &record, true
}

// ClearDisableRecord удаляет запись о disable (после успешного enable).
func (s *RedisStore) ClearDisableRecord(ctx context.Context, internalID int64) error {
	key := fmt.Sprintf("rw:disable:%d", internalID)
	return s.client.Del(ctx, key).Err()
}
