package metrics

import (
	"blocker-worker/internal/logger"
	"fmt"
	"sync/atomic"
	"time"
)

// Counters содержит все метрики приложения.
type Counters struct {
	// RabbitMQ metrics
	MessagesReceived atomic.Int64
	MessagesAck      atomic.Int64
	MessagesNack     atomic.Int64

	// NFT command metrics
	NftCommandsTotal   atomic.Int64
	NftCommandsSuccess atomic.Int64
	NftCommandsFail    atomic.Int64
	NftCommandsTimeout atomic.Int64

	// Validation metrics
	InvalidIPsCount atomic.Int64

	// Pool metrics
	PoolQueueFullWarnings atomic.Int64
}

// Global instance
var global = &Counters{}

// Get возвращает глобальный экземпляр счетчиков.
func Get() *Counters {
	return global
}

// StartDumper запускает горутину, которая периодически логирует метрики.
func StartDumper(l *logger.Logger, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			dumpMetrics(l)
		}
	}()
}

// dumpMetrics выводит текущие значения всех счетчиков.
func dumpMetrics(l *logger.Logger) {
	msg := fmt.Sprintf(
		"[Metrics] "+
			"RabbitMQ: recv=%d ack=%d nack=%d | "+
			"NFT: total=%d ok=%d fail=%d timeout=%d | "+
			"Validation: invalid_ips=%d | "+
			"Pool: queue_full_warnings=%d",
		global.MessagesReceived.Load(),
		global.MessagesAck.Load(),
		global.MessagesNack.Load(),
		global.NftCommandsTotal.Load(),
		global.NftCommandsSuccess.Load(),
		global.NftCommandsFail.Load(),
		global.NftCommandsTimeout.Load(),
		global.InvalidIPsCount.Load(),
		global.PoolQueueFullWarnings.Load(),
	)
	l.Info(msg)
}
