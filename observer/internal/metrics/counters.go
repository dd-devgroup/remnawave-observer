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
	RequestsTotal              atomic.Int64
	RejectedRequestsTotal      atomic.Int64
	GeoIPLookupSuccess         atomic.Int64
	GeoIPLookupFail            atomic.Int64
	GeoIPLookupTimeout         atomic.Int64
	GeoIPFallbackLookupSuccess atomic.Int64
	GeoIPFallbackLookupFail    atomic.Int64
	GeoIPFallbackLookupTimeout atomic.Int64
	SideEffectTimeoutCount     atomic.Int64
	ScanPartialRunsCount       atomic.Int64

	// Remnawave Enforcement метрики (MIG-5)
	RemnawaveDisableOk     atomic.Int64
	RemnawaveDisableFail   atomic.Int64
	RemnawaveEnableOk      atomic.Int64
	RemnawaveEnableFail    atomic.Int64
	UUIDCacheHit           atomic.Int64
	UUIDCacheMiss          atomic.Int64
	UserResolveCacheHit    atomic.Int64
	UserResolveCacheMiss   atomic.Int64
	PanelFetchSubmitOk     atomic.Int64
	PanelFetchSubmitFail   atomic.Int64
	PanelFetchResultOk     atomic.Int64
	PanelFetchResultFail   atomic.Int64
	PanelFetchTimeout      atomic.Int64
	PanelNodesNoData       atomic.Int64
	PanelDedupHit          atomic.Int64
	ExecutorBlockOk        atomic.Int64
	ExecutorBlockFail      atomic.Int64
	DiscardedUnspecifiedIP atomic.Int64
	ExcludedSquadUserTotal atomic.Int64
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
			log.Printf("[metrics] requests_total=%d rejected=%d geoip_ok=%d geoip_fail=%d geoip_timeout=%d geo_fallback_ok=%d geo_fallback_fail=%d geo_fallback_timeout=%d sideeffect_timeout=%d scan_partial=%d rw_disable_ok=%d rw_disable_fail=%d rw_enable_ok=%d rw_enable_fail=%d uuid_cache_hit=%d uuid_cache_miss=%d user_resolve_cache_hit=%d user_resolve_cache_miss=%d panel_submit_ok=%d panel_submit_fail=%d panel_result_ok=%d panel_result_fail=%d panel_timeout=%d panel_no_data=%d panel_dedup_hit=%d executor_block_ok=%d executor_block_fail=%d discarded_unspecified_ip=%d excluded_squad_users=%d",
				RequestsTotal.Load(), RejectedRequestsTotal.Load(),
				GeoIPLookupSuccess.Load(), GeoIPLookupFail.Load(), GeoIPLookupTimeout.Load(),
				GeoIPFallbackLookupSuccess.Load(), GeoIPFallbackLookupFail.Load(), GeoIPFallbackLookupTimeout.Load(),
				SideEffectTimeoutCount.Load(), ScanPartialRunsCount.Load(),
				RemnawaveDisableOk.Load(), RemnawaveDisableFail.Load(),
				RemnawaveEnableOk.Load(), RemnawaveEnableFail.Load(),
				UUIDCacheHit.Load(), UUIDCacheMiss.Load(),
				UserResolveCacheHit.Load(), UserResolveCacheMiss.Load(),
				PanelFetchSubmitOk.Load(), PanelFetchSubmitFail.Load(),
				PanelFetchResultOk.Load(), PanelFetchResultFail.Load(),
				PanelFetchTimeout.Load(), PanelNodesNoData.Load(),
				PanelDedupHit.Load(), ExecutorBlockOk.Load(), ExecutorBlockFail.Load(),
				DiscardedUnspecifiedIP.Load(), ExcludedSquadUserTotal.Load())
		case <-ctx.Done():
			return
		}
	}
}
