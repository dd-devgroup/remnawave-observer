package processor

import (
	"context"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/enforcement"
	"testing"
	"time"
)

// Mock implementations для тестирования
type MockStorage struct {
	checkAndAddSubnetCalls int
}

func (m *MockStorage) CheckAndAddIP(ctx context.Context, email, ip string, limit int, ttl, cooldown time.Duration) (*models.CheckResult, error) {
	return &models.CheckResult{StatusCode: 0, CurrentCount: 1, IsNew: true}, nil
}

func (m *MockStorage) CheckAndAddSubnet(ctx context.Context, email, subnet string, limit int, ttl, cooldown time.Duration) (*models.CheckResult, error) {
	m.checkAndAddSubnetCalls++
	return &models.CheckResult{
		StatusCode:   0,
		CurrentCount: int64(m.checkAndAddSubnetCalls),
		IsNew:        true,
	}, nil
}

func (m *MockStorage) ClearUserIPs(ctx context.Context, email string) (int, error) {
	return 0, nil
}

func (m *MockStorage) GetUserActiveIPs(ctx context.Context, userEmail string) (map[string]int, error) {
	return make(map[string]int), nil
}

func (m *MockStorage) GetAllUserEmails(ctx context.Context) ([]string, error) {
	return []string{}, nil
}

func (m *MockStorage) HasAlertCooldown(ctx context.Context, userEmail string) (bool, error) {
	return false, nil
}

func (m *MockStorage) Ping(ctx context.Context) error {
	return nil
}

func (m *MockStorage) Close() error {
	return nil
}

func (m *MockStorage) ClearUserSubnets(ctx context.Context, email string) (int, error) {
	return 0, nil
}

func (m *MockStorage) GetUserActiveSubnets(ctx context.Context, userEmail string) (map[string]int, error) {
	return make(map[string]int), nil
}

func (m *MockStorage) GetUserActiveASNs(ctx context.Context, userEmail string) (map[string]*models.ASNInfo, error) {
	return make(map[string]*models.ASNInfo), nil
}

type MockPublisher struct {
	publishedMessages int
}

func (m *MockPublisher) PublishBlockMessage(msg models.BlockMessage) error {
	m.publishedMessages++
	return nil
}

func (m *MockPublisher) Close() error {
	return nil
}

func (m *MockPublisher) Ping() error {
	return nil
}

type MockAlerter struct {
	alertsSent int
}

func (m *MockAlerter) SendAlert(ctx context.Context, payload models.AlertPayload) error {
	m.alertsSent++
	return nil
}

func TestLogProcessor_ASNMode_Initialization(t *testing.T) {
	cfg := &config.Config{
		DetectByASN:               true,
		MaxASNsPerUser:            4,
		UserSubnetTTL:             time.Hour,
		AlertCooldown:             time.Minute,
		ClearIPsDelay:             30 * time.Second,
		LogChannelBufferSize:      10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:            2,
		SideEffectWorkerPoolSize:  2,
		ExcludedUsers:             make(map[string]bool),
		ExcludedASNs:              make(map[string]bool),
	}

	storage := &MockStorage{}
	publisher := &MockPublisher{}
	alerter := &MockAlerter{}

	// Без ASN lookup (будет использован fallback)
	processor := NewLogProcessor(storage, publisher, enforcement.NewNoopEnforcer(), alerter, cfg, nil, nil, nil, nil, nil)

	if processor.cfg.DetectByASN != true {
		t.Error("Expected ASN mode to be enabled")
	}

	if processor.cfg.MaxASNsPerUser != 4 {
		t.Errorf("Expected MaxASNsPerUser to be 4, got %d", processor.cfg.MaxASNsPerUser)
	}
}

func TestLogProcessor_ASNMode_ProcessEntry(t *testing.T) {
	ctx := context.Background()

	cfg := &config.Config{
		DetectByASN:                 true,
		MaxASNsPerUser:              4,
		ASNFallbackMask:             16,
		UserSubnetTTL:               time.Hour,
		AlertCooldown:               time.Minute,
		ClearIPsDelay:               30 * time.Second,
		LogChannelBufferSize:        10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs:                make(map[string]bool),
	}

	storage := &MockStorage{}
	publisher := &MockPublisher{}
	alerter := &MockAlerter{}

	processor := NewLogProcessor(storage, publisher, enforcement.NewNoopEnforcer(), alerter, cfg, nil, nil, nil, nil, nil)

	entry := models.LogEntry{
		UserEmail: "test@example.com",
		SourceIP:  "8.8.8.8",
	}

	// Обрабатываем запись
	processor.processSingleEntry(ctx, entry)

	// Проверяем что был вызван CheckAndAddSubnet (так как ASN не найден, используется fallback)
	if storage.checkAndAddSubnetCalls != 1 {
		t.Errorf("Expected CheckAndAddSubnet to be called once, got %d", storage.checkAndAddSubnetCalls)
	}
}

func TestLogProcessor_ASNMode_ExcludedASN(t *testing.T) {
	ctx := context.Background()

	cfg := &config.Config{
		DetectByASN:                 true,
		MaxASNsPerUser:              4,
		UserSubnetTTL:               time.Hour,
		AlertCooldown:               time.Minute,
		ClearIPsDelay:               30 * time.Second,
		LogChannelBufferSize:        10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs: map[string]bool{
			"AS15169": true, // Google
		},
	}

	storage := &MockStorage{}
	publisher := &MockPublisher{}
	alerter := &MockAlerter{}

	// Примечание: Для полного теста нужна реальная ASN база
	// Здесь тестируем логику без реального lookup
	processor := NewLogProcessor(storage, publisher, enforcement.NewNoopEnforcer(), alerter, cfg, nil, nil, nil, nil, nil)

	entry := models.LogEntry{
		UserEmail: "test@example.com",
		SourceIP:  "8.8.8.8", // Google DNS
	}

	processor.processSingleEntry(ctx, entry)

	// Без реального ASN lookup будет использован fallback
	// Тест демонстрирует структуру, для полного теста нужна база
}

func TestLogProcessor_FallbackBehavior(t *testing.T) {
	ctx := context.Background()

	cfg := &config.Config{
		DetectByASN:                 true,
		MaxASNsPerUser:              4,
		ASNFallbackMask:             16,
		UserSubnetTTL:               time.Hour,
		AlertCooldown:               time.Minute,
		ClearIPsDelay:               30 * time.Second,
		LogChannelBufferSize:        10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs:                make(map[string]bool),
	}

	storage := &MockStorage{}
	publisher := &MockPublisher{}
	alerter := &MockAlerter{}

	processor := NewLogProcessor(storage, publisher, enforcement.NewNoopEnforcer(), alerter, cfg, nil, nil, nil, nil, nil)

	testIPs := []string{
		"176.59.40.10",
		"176.59.172.25",
		"176.59.164.100",
	}

	// Все эти IP должны попасть в одну /16 подсеть при fallback
	for _, ip := range testIPs {
		entry := models.LogEntry{
			UserEmail: "test@example.com",
			SourceIP:  ip,
		}
		processor.processSingleEntry(ctx, entry)
	}

	// Должны быть вызовы CheckAndAddSubnet
	if storage.checkAndAddSubnetCalls == 0 {
		t.Error("Expected CheckAndAddSubnet to be called for fallback")
	}
}

// Примечание: Для полноценного тестирования ASN режима нужна реальная база GeoLite2-ASN.mmdb
// Эти тесты демонстрируют структуру и логику работы без внешних зависимостей
