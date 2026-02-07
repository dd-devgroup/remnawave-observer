package config

import (
	"os"
	"strconv"
	"time"
)

const (
	defaultRabbitMQURL    = "amqp://guest:guest@localhost/"
	reconnectDelay        = 5 * time.Second
	defaultBlockerWorkers = 16
	defaultQueueSize      = 1000
	defaultDrainTimeout   = 10 * time.Second
	defaultNftTimeout     = 3 * time.Second
	defaultPrefetchCount  = 0 // 0 = auto (будет BlockerWorkers * 2)
)

// Config хранит конфигурацию приложения.
type Config struct {
	RabbitMQURL    string
	ReconnectDelay time.Duration

	// Worker pool settings
	BlockerWorkers int           // Количество воркеров для обработки nft команд
	QueueSize      int           // Размер буфера очереди задач
	DrainTimeout   time.Duration // Таймаут на drain очереди при shutdown

	// NFT command settings
	NftTimeout time.Duration // Таймаут на выполнение одной nft команды

	// RabbitMQ consumer settings
	PrefetchCount int // QoS prefetch count (0 = auto = BlockerWorkers * 2)
}

// getEnvInt возвращает значение переменной окружения как int, или defaultVal при ошибке.
func getEnvInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(val)
	if err != nil || i <= 0 {
		return defaultVal
	}
	return i
}

// getEnvDuration возвращает значение переменной окружения как duration (секунды), или defaultVal при ошибке.
func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(val)
	if err != nil || i <= 0 {
		return defaultVal
	}
	return time.Duration(i) * time.Second
}

// New создает новый экземпляр Config из переменных окружения.
func New() *Config {
	rabbitmqURL := os.Getenv("RABBITMQ_URL")
	if rabbitmqURL == "" {
		rabbitmqURL = defaultRabbitMQURL
	}

	blockerWorkers := getEnvInt("BLOCKER_WORKERS", defaultBlockerWorkers)
	prefetchCount := getEnvInt("PREFETCH_COUNT", defaultPrefetchCount)

	// Если prefetch не задан (0), устанавливаем BlockerWorkers * 2
	if prefetchCount == 0 {
		prefetchCount = blockerWorkers * 2
	}

	return &Config{
		RabbitMQURL:    rabbitmqURL,
		ReconnectDelay: reconnectDelay,
		BlockerWorkers: blockerWorkers,
		QueueSize:      getEnvInt("BLOCKER_QUEUE_SIZE", defaultQueueSize),
		DrainTimeout:   defaultDrainTimeout,
		NftTimeout:     getEnvDuration("NFT_TIMEOUT_SECONDS", defaultNftTimeout),
		PrefetchCount:  prefetchCount,
	}
}