package metrics

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Все счётчики — глобальные атомики. Инкрементируются в точках наблюдения;
// периодически сливаются в лог через StartDumper.
var (
	RequestsTotal          atomic.Int64
	RejectedRequestsTotal  atomic.Int64
	RabbitPublishSuccess   atomic.Int64
	RabbitPublishFail      atomic.Int64
	RabbitPublishRetry     atomic.Int64
	GeoIPLookupSuccess     atomic.Int64
	GeoIPLookupFail        atomic.Int64
	GeoIPLookupTimeout     atomic.Int64
	SideEffectTimeoutCount atomic.Int64
	ScanPartialRunsCount   atomic.Int64

	// Remnawave Enforcement метрики (MIG-5)
	RemnawaveDisableOk   atomic.Int64
	RemnawaveDisableFail atomic.Int64
	RemnawaveEnableOk    atomic.Int64
	RemnawaveEnableFail  atomic.Int64
	UUIDCacheHit         atomic.Int64
	UUIDCacheMiss        atomic.Int64
)

// StartDumper запускает периодический сброс счётчиков в лог.
// Блокирует вызывающую горутину до отмены ctx; при завершении вызывает wg.Done().
func StartDumper(ctx context.Context, wg *sync.WaitGroup, interval time.Duration) {
	defer wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			log.Printf("[metrics] requests_total=%d rejected=%d rabbit_pub_ok=%d rabbit_pub_fail=%d rabbit_pub_retry=%d geoip_ok=%d geoip_fail=%d geoip_timeout=%d sideeffect_timeout=%d scan_partial=%d rw_disable_ok=%d rw_disable_fail=%d rw_enable_ok=%d rw_enable_fail=%d uuid_cache_hit=%d uuid_cache_miss=%d",
				RequestsTotal.Load(), RejectedRequestsTotal.Load(),
				RabbitPublishSuccess.Load(), RabbitPublishFail.Load(), RabbitPublishRetry.Load(),
				GeoIPLookupSuccess.Load(), GeoIPLookupFail.Load(), GeoIPLookupTimeout.Load(),
				SideEffectTimeoutCount.Load(), ScanPartialRunsCount.Load(),
				RemnawaveDisableOk.Load(), RemnawaveDisableFail.Load(),
				RemnawaveEnableOk.Load(), RemnawaveEnableFail.Load(),
				UUIDCacheHit.Load(), UUIDCacheMiss.Load())
		case <-ctx.Done():
			return
		}
	}
}
