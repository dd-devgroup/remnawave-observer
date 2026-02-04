package processor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/alerter"
	"observer_service/internal/services/asn"
	"observer_service/internal/services/geoip"
	"observer_service/internal/services/publisher"
	"observer_service/internal/services/scoring"
	"observer_service/internal/services/storage"
	"sync"
	"time"
)

// LogProcessor обрабатывает входящие логи.
type LogProcessor struct {
	storage           storage.IPStorage
	publisher         publisher.EventPublisher
	alerter           alerter.Notifier
	cfg               *config.Config
	asnLookup         *asn.ASNLookup         // Сервис для lookup ASN
	logChannel        chan []models.LogEntry // Канал для получения пачек логов
	sideEffectChannel chan func(context.Context) // Канал для побочных задач (алерты, очистка)

	// Новые сервисы для Anti-Abuse системы
	geoService    *geoip.GeoIPService   // Сервис геолокации
	geoAnalyzer   *geoip.GeoAnalyzer    // Анализатор географии
	asnClassifier *asn.ASNClassifier    // Классификатор провайдеров
	scorer        *scoring.Scorer       // Система скоринга

	// Кешированные распарсенные подсети для быстрой проверки вложенности
	excludedSubnetsParsed []*net.IPNet
	excludedIPsParsed     []*net.IPNet // Для случаев когда в ExcludedIPs указан CIDR
}

// NewLogProcessor создает новый экземпляр LogProcessor.
func NewLogProcessor(
	s storage.IPStorage,
	p publisher.EventPublisher,
	a alerter.Notifier,
	cfg *config.Config,
	asnLookup *asn.ASNLookup,
	geoService *geoip.GeoIPService,
	geoAnalyzer *geoip.GeoAnalyzer,
	asnClassifier *asn.ASNClassifier,
	scorer *scoring.Scorer,
) *LogProcessor {
	lp := &LogProcessor{
		storage:           s,
		publisher:         p,
		alerter:           a,
		cfg:               cfg,
		asnLookup:         asnLookup,
		geoService:        geoService,
		geoAnalyzer:       geoAnalyzer,
		asnClassifier:     asnClassifier,
		scorer:            scorer,
		logChannel:        make(chan []models.LogEntry, cfg.LogChannelBufferSize),
		sideEffectChannel: make(chan func(context.Context), cfg.SideEffectChannelBufferSize),
	}

	// Парсим исключённые подсети один раз при инициализации
	for subnetStr := range cfg.ExcludedSubnets {
		_, ipNet, err := net.ParseCIDR(subnetStr)
		if err != nil {
			log.Printf("Предупреждение: не удалось распарсить исключённую подсеть '%s': %v", subnetStr, err)
			continue
		}
		lp.excludedSubnetsParsed = append(lp.excludedSubnetsParsed, ipNet)
	}
	if len(lp.excludedSubnetsParsed) > 0 {
		log.Printf("Распарсено %d исключённых подсетей для проверки вложенности", len(lp.excludedSubnetsParsed))
	}

	// Парсим исключённые IP (поддержка CIDR в EXCLUDED_IPS)
	for ipStr := range cfg.ExcludedIPs {
		// Пробуем распарсить как CIDR
		if _, ipNet, err := net.ParseCIDR(ipStr); err == nil {
			lp.excludedIPsParsed = append(lp.excludedIPsParsed, ipNet)
		} else if ip := net.ParseIP(ipStr); ip != nil {
			// Одиночный IP -> преобразуем в /32 CIDR
			var mask net.IPMask
			if ip.To4() != nil {
				mask = net.CIDRMask(32, 32)
			} else {
				mask = net.CIDRMask(128, 128)
			}
			lp.excludedIPsParsed = append(lp.excludedIPsParsed, &net.IPNet{IP: ip, Mask: mask})
		} else {
			log.Printf("Предупреждение: не удалось распарсить исключённый IP '%s'", ipStr)
		}
	}
	if len(lp.excludedIPsParsed) > 0 {
		log.Printf("Распарсено %d исключённых IP/CIDR для проверки вложенности", len(lp.excludedIPsParsed))
	}

	return lp
}

// StartWorkerPool запускает пул горутин-воркеров для обработки логов.
func (p *LogProcessor) StartWorkerPool(ctx context.Context, mainWg *sync.WaitGroup) {
	defer mainWg.Done()

	var workerWg sync.WaitGroup
	log.Printf("Запуск пула воркеров обработки логов в количестве %d...", p.cfg.WorkerPoolSize)

	for i := 0; i < p.cfg.WorkerPoolSize; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			log.Printf("Воркер обработки логов %d запущен", workerID)
			for entries := range p.logChannel {
				p.ProcessEntries(ctx, entries)
			}
			log.Printf("Воркер обработки логов %d останавливается.", workerID)
		}(i + 1)
	}

	<-ctx.Done()
	log.Println("Получен сигнал остановки для воркеров обработки логов. Закрываю канал...")
	close(p.logChannel)
	workerWg.Wait()
	log.Println("Все воркеры обработки логов успешно остановлены.")
}

// StartSideEffectWorkerPool запускает пул воркеров для выполнения побочных задач.
func (p *LogProcessor) StartSideEffectWorkerPool(ctx context.Context, mainWg *sync.WaitGroup) {
	defer mainWg.Done()

	var workerWg sync.WaitGroup
	log.Printf("Запуск пула воркеров побочных задач в количестве %d...", p.cfg.SideEffectWorkerPoolSize)

	for i := 0; i < p.cfg.SideEffectWorkerPoolSize; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			log.Printf("Воркер побочных задач %d запущен", workerID)
			for task := range p.sideEffectChannel {
				select {
				case <-ctx.Done():
					log.Printf("Воркер побочных задач %d пропустил задачу из-за отмены контекста.", workerID)
				default:
					taskCtx, cancel := context.WithTimeout(ctx, p.cfg.SideEffectTimeout)
					task(taskCtx)
					cancel()
				}
			}
			log.Printf("Воркер побочных задач %d останавливается.", workerID)
		}(i + 1)
	}

	<-ctx.Done()
	log.Println("Получен сигнал остановки для воркеров побочных задач. Закрываю канал...")
	close(p.sideEffectChannel)
	workerWg.Wait()
	log.Println("Все воркеры побочных задач успешно остановлены.")
}

// EnqueueEntries добавляет пачку логов в очередь на обработку.
func (p *LogProcessor) EnqueueEntries(entries []models.LogEntry) error {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Попытка записи в закрытый канал логов. Сервис находится в процессе остановки.")
		}
	}()

	select {
	case p.logChannel <- entries:
		return nil
	default:
		return errors.New("log channel is full, rejecting new entries")
	}
}

// enqueueSideEffectTask добавляет побочную задачу в очередь на выполнение.
func (p *LogProcessor) enqueueSideEffectTask(task func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Попытка записи в закрытый канал побочных задач. Сервис находится в процессе остановки.")
		}
	}()

	select {
	case p.sideEffectChannel <- task:
		// Задача успешно добавлена в очередь
	default:
		log.Println("Warning: очередь побочных задач заполнена. Задача отброшена.")
	}
}

// ProcessEntries обрабатывает пачку записей логов.
func (p *LogProcessor) ProcessEntries(ctx context.Context, entries []models.LogEntry) {
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			log.Printf("Обработка пачки прервана из-за отмены контекста: %v", ctx.Err())
			return
		default:
			p.processSingleEntry(ctx, entry)
		}
	}
}

func (p *LogProcessor) processSingleEntry(ctx context.Context, entry models.LogEntry) {
	if p.cfg.ExcludedUsers[entry.UserEmail] {
		return // Пользователь в списке исключений
	}

	if p.cfg.DetectByASN {
		p.processEntryByASN(ctx, entry)
	} else if p.cfg.DetectBySubnet {
		p.processEntryBySubnet(ctx, entry)
	} else {
		p.processEntryByIP(ctx, entry)
	}
}

func (p *LogProcessor) processEntryByIP(ctx context.Context, entry models.LogEntry) {
	userIPLimit := p.getUserIPLimit(entry.UserEmail)
	debugMarker := p.getDebugMarker(entry.UserEmail)

	res, err := p.storage.CheckAndAddIP(ctx, entry.UserEmail, entry.SourceIP, userIPLimit, p.cfg.UserIPTTL, p.cfg.AlertCooldown)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("Операция CheckAndAddIP отменена для %s: %v", entry.UserEmail, err)
		} else {
			log.Printf("Ошибка обработки записи для %s: %v", entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode == 0 && res.IsNew {
		log.Printf("Новый IP для пользователя %s%s: %s. Всего IP: %d/%d",
			entry.UserEmail, debugMarker, entry.SourceIP, res.CurrentCount, userIPLimit)
	}

	if res.StatusCode == 1 { // Лимит превышен, нужна блокировка
		log.Printf("ПРЕВЫШЕНИЕ ЛИМИТА IP%s: Пользователь %s, IP-адресов: %d/%d",
			debugMarker, entry.UserEmail, res.CurrentCount, userIPLimit)

		ipsToBlock := p.filterExcludedIPs(res.AllUserItems, entry.UserEmail)

		if len(ipsToBlock) > 0 {
			if err := p.publishBlockEvent(ipsToBlock, p.cfg.BlockDuration); err != nil {
				log.Printf("Ошибка отправки сообщения о блокировке: %v", err)
			} else {
				log.Printf("Сообщение о блокировке %d IP-адресов для %s%s отправлено", len(ipsToBlock), entry.UserEmail, debugMarker)
				p.enqueueSideEffectTask(func(ctx context.Context) {
					p.scheduleIPsClear(ctx, entry.UserEmail)
				})
			}
		}

		ipCount := int(res.CurrentCount)
		alertPayload := models.AlertPayload{
			UserIdentifier:   entry.UserEmail,
			DetectedIPsCount: &ipCount,
			Limit:            userIPLimit,
			AllUserIPs:       res.AllUserItems,
			BlockDuration:    p.cfg.BlockDuration,
			ViolationType:    "ip_limit_exceeded",
		}
		p.enqueueSideEffectTask(func(ctx context.Context) {
			if err := p.alerter.SendAlert(ctx, alertPayload); err != nil {
				log.Printf("Ошибка отправки вебхук-уведомления: %v", err)
			}
		})
	}
}

func (p *LogProcessor) processEntryBySubnet(ctx context.Context, entry models.LogEntry) {
	ip := net.ParseIP(entry.SourceIP)
	if ip == nil || ip.To4() == nil {
		return // Игнорируем невалидные или не-IPv4 адреса
	}

	mask := net.CIDRMask(p.cfg.SubnetMaskIPv4, 32)
	subnet := &net.IPNet{IP: ip.Mask(mask), Mask: mask}
	subnetStr := subnet.String()

	userSubnetLimit := p.cfg.MaxSubnetsPerUser
	debugMarker := p.getDebugMarker(entry.UserEmail)

	res, err := p.storage.CheckAndAddSubnet(ctx, entry.UserEmail, subnetStr, userSubnetLimit, p.cfg.UserSubnetTTL, p.cfg.AlertCooldown)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("Операция CheckAndAddSubnet отменена для %s: %v", entry.UserEmail, err)
		} else {
			log.Printf("Ошибка обработки записи (Subnet) для %s: %v", entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode == 0 && res.IsNew {
		log.Printf("Новая подсеть для пользователя %s%s: %s. Всего подсетей: %d/%d",
			entry.UserEmail, debugMarker, subnetStr, res.CurrentCount, userSubnetLimit)
	}

	if res.StatusCode == 1 { // Лимит превышен, нужна блокировка
		log.Printf("ПРЕВЫШЕНИЕ ЛИМИТА ПОДСЕТЕЙ%s: Пользователь %s, подсетей: %d/%d",
			debugMarker, entry.UserEmail, res.CurrentCount, userSubnetLimit)

		// Фильтруем подсети из белого списка перед блокировкой
		subnetsToBlock := p.filterExcludedSubnets(res.AllUserItems, entry.UserEmail)

		if len(subnetsToBlock) > 0 {
			if err := p.publishBlockEvent(subnetsToBlock, p.cfg.BlockDuration); err != nil {
				log.Printf("Ошибка отправки сообщения о блокировке подсетей: %v", err)
			} else {
				log.Printf("Сообщение о блокировке %d подсетей для %s%s отправлено", len(subnetsToBlock), entry.UserEmail, debugMarker)
				p.enqueueSideEffectTask(func(ctx context.Context) {
					p.scheduleSubnetsClear(ctx, entry.UserEmail)
				})
			}
		}

		subnetCount := int(res.CurrentCount)
		alertPayload := models.AlertPayload{
			UserIdentifier:   entry.UserEmail,
			DetectedIPsCount: &subnetCount,
			Limit:            userSubnetLimit,
			AllUserIPs:       res.AllUserItems, // В алерт отправляем все подсети, даже исключенные
			BlockDuration:    p.cfg.BlockDuration,
			ViolationType:    "subnet_limit_exceeded",
		}
		p.enqueueSideEffectTask(func(ctx context.Context) {
			if err := p.alerter.SendAlert(ctx, alertPayload); err != nil {
				log.Printf("Ошибка отправки вебхук-уведомления: %v", err)
			}
		})
	}
}

func (p *LogProcessor) getUserIPLimit(userEmail string) int {
	if p.cfg.DebugEmail != "" && userEmail == p.cfg.DebugEmail {
		return p.cfg.DebugIPLimit
	}
	return p.cfg.MaxIPsPerUser
}

func (p *LogProcessor) getDebugMarker(userEmail string) string {
	if p.cfg.DebugEmail != "" && userEmail == p.cfg.DebugEmail {
		return " [DEBUG]"
	}
	return ""
}

func (p *LogProcessor) filterExcludedIPs(ips []string, email string) []string {
	if len(p.excludedIPsParsed) == 0 {
		return ips
	}

	var filtered []string
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			// Невалидный IP — пропускаем в блокировку (пусть nft разбирается)
			filtered = append(filtered, ipStr)
			continue
		}

		excluded := false
		for _, excludedNet := range p.excludedIPsParsed {
			if excludedNet.Contains(ip) {
				log.Printf("IP-адрес %s для пользователя %s пропущен (входит в исключённую сеть %s)", ipStr, email, excludedNet.String())
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, ipStr)
		}
	}
	return filtered
}

// filterExcludedSubnets проверяет список подсетей на вложенность в белый список.
// Подсеть считается исключённой, если она полностью входит в любую из исключённых подсетей.
func (p *LogProcessor) filterExcludedSubnets(subnets []string, email string) []string {
	if len(p.excludedSubnetsParsed) == 0 {
		return subnets
	}

	var filtered []string
	for _, subnetStr := range subnets {
		_, subnetNet, err := net.ParseCIDR(subnetStr)
		if err != nil {
			// Невалидная подсеть — пропускаем в блокировку
			filtered = append(filtered, subnetStr)
			continue
		}

		excluded := false
		for _, excludedNet := range p.excludedSubnetsParsed {
			// Проверяем: входит ли первый IP подсети в исключённую сеть
			// Это означает, что subnetNet является подмножеством excludedNet
			if excludedNet.Contains(subnetNet.IP) {
				// Дополнительная проверка: маска subnetNet должна быть >= маски excludedNet
				// (т.е. subnetNet должна быть меньше или равна excludedNet)
				excludedOnes, _ := excludedNet.Mask.Size()
				subnetOnes, _ := subnetNet.Mask.Size()
				if subnetOnes >= excludedOnes {
					log.Printf("Подсеть %s для пользователя %s пропущена (входит в исключённую сеть %s)", subnetStr, email, excludedNet.String())
					excluded = true
					break
				}
			}
		}
		if !excluded {
			filtered = append(filtered, subnetStr)
		}
	}
	return filtered
}

// processEntryByASN обрабатывает подключение в режиме ASN (по провайдерам)
func (p *LogProcessor) processEntryByASN(ctx context.Context, entry models.LogEntry) {
	var identifier string
	var identifierType string
	var orgName string

	var redisStore *storage.RedisStore

	// Пытаемся получить ASN для IP
	if p.asnLookup != nil {
		redisStore = p.storage.(*storage.RedisStore)
		asnStr, org, err := p.asnLookup.LookupWithOrg(entry.SourceIP)
		if err == nil && asnStr != "" {
			// Проверяем, не в списке ли исключённых ASN
			if p.cfg.ExcludedASNs[asnStr] {
				log.Printf("IP %s (ASN %s - %s) в списке исключённых ASN, пропускаем", entry.SourceIP, asnStr, org)
				return
			}
			identifier = asnStr
			identifierType = "ASN"
			orgName = org

			// Кешируем название организации для последующего использования
			if org != "" {
				if err := redisStore.SetASNOrgName(ctx, asnStr, org, p.cfg.UserSubnetTTL); err != nil {
					log.Printf("Ошибка кеширования org для ASN %s: %v", asnStr, err)
				}
			}
		} else {
			// Логируем для дебага, но продолжаем с fallback
			if err != nil {
				log.Printf("Не удалось определить ASN для IP %s (пользователь %s): %v. Используем fallback.",
					entry.SourceIP, entry.UserEmail, err)
			}
		}
	}

	// Fallback на подсеть если ASN не найден или сервис недоступен
	if identifier == "" {
		ip := net.ParseIP(entry.SourceIP)
		if ip == nil || ip.To4() == nil {
			log.Printf("Невалидный IP-адрес %s для пользователя %s, пропускаем", entry.SourceIP, entry.UserEmail)
			return
		}
		mask := net.CIDRMask(p.cfg.ASNFallbackMask, 32)
		subnet := &net.IPNet{IP: ip.Mask(mask), Mask: mask}
		identifier = subnet.String()
		identifierType = "Subnet"
	}

	userASNLimit := p.cfg.MaxASNsPerUser
	debugMarker := p.getDebugMarker(entry.UserEmail)

	// ВАЖНО: Сохраняем связь ASN -> IP ДО проверки лимита
	// Это гарантирует что IP будет в Redis когда мы соберём данные для блокировки
	if identifierType == "ASN" {
		if err := redisStore.AddIPToASNMapping(ctx, entry.UserEmail, identifier, entry.SourceIP, p.cfg.UserSubnetTTL); err != nil {
			log.Printf("Ошибка сохранения связи ASN->IP для %s: %v. Пропускаем обработку.", entry.UserEmail, err)
			return // Прерываем если не удалось сохранить IP - иначе ASN будет без IP
		}
	}

	// Для ASN используем специальный метод CheckAndAddASN с фильтрацией "мертвых" ASN
	// Для Subnet fallback используем стандартный CheckAndAddSubnet
	var res *models.CheckResult
	var err error
	if identifierType == "ASN" {
		res, err = redisStore.CheckAndAddASN(ctx, entry.UserEmail, identifier, userASNLimit, p.cfg.UserSubnetTTL, p.cfg.AlertCooldown)
	} else {
		res, err = p.storage.CheckAndAddSubnet(ctx, entry.UserEmail, identifier, userASNLimit, p.cfg.UserSubnetTTL, p.cfg.AlertCooldown)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("Операция CheckAndAdd%s отменена для %s: %v", identifierType, entry.UserEmail, err)
		} else {
			log.Printf("Ошибка обработки записи (%s) для %s: %v", identifierType, entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode == 0 && res.IsNew {
		if identifierType == "ASN" && orgName != "" {
			// Получаем GeoIP данные ОДИН раз для классификатора и логирования
			var countryCode string
			var geoLoc *geoip.GeoLocation
			if p.geoService != nil && p.cfg.GeoIPEnabled {
				if loc, err := p.geoService.Lookup(ctx, entry.SourceIP); err == nil && loc != nil {
					geoLoc = loc
					countryCode = loc.CountryCode
				}
			}

			// Классифицируем провайдера для логирования с учетом страны
			var providerInfo string
			if p.asnClassifier != nil {
				classification := p.asnClassifier.ClassifyWithCountry(identifier, orgName, countryCode)
				providerInfo = fmt.Sprintf(" [%s, риск:%.1f]", classification.ProviderType, classification.Modifier)
			}
			log.Printf("Новый %s для пользователя %s%s: %s (%s)%s | IP: %s. Всего: %d/%d",
				identifierType, entry.UserEmail, debugMarker, identifier, orgName, providerInfo, entry.SourceIP, res.CurrentCount, userASNLimit)

			// Логируем GeoIP анализ (используем уже полученные данные)
			if geoLoc != nil {
				log.Printf("[GeoIP] %s: %s -> %s, %s (%.2f, %.2f)",
					entry.UserEmail, entry.SourceIP, geoLoc.CountryCode, geoLoc.City,
					geoLoc.Latitude, geoLoc.Longitude)
			}
		} else {
			log.Printf("Новый %s для пользователя %s%s: %s | IP: %s. Всего: %d/%d",
				identifierType, entry.UserEmail, debugMarker, identifier, entry.SourceIP, res.CurrentCount, userASNLimit)
		}
	}

	if res.StatusCode == 1 { // Лимит превышен, нужна блокировка
		log.Printf("⚠️  ПРЕВЫШЕНИЕ ЛИМИТА %s%s: Пользователь %s, кол-во: %d/%d",
			identifierType, debugMarker, entry.UserEmail, res.CurrentCount, userASNLimit)

		// Собираем все IP-адреса для блокировки
		// Передаём текущий IP чтобы гарантировать его включение даже если Redis ещё не обновился
		ipsToBlock := p.collectIPsForASNBlock(ctx, entry.UserEmail, res.AllUserItems, entry.SourceIP)

		// Фильтруем исключенные подсети/IP
		if identifierType == "Subnet" {
			ipsToBlock = p.filterExcludedSubnets(ipsToBlock, entry.UserEmail)
		} else {
			ipsToBlock = p.filterExcludedIPs(ipsToBlock, entry.UserEmail)
		}

		if len(ipsToBlock) > 0 {
			if err := p.publishBlockEvent(ipsToBlock, p.cfg.BlockDuration); err != nil {
				log.Printf("Ошибка отправки сообщения о блокировке: %v", err)
			} else {
				log.Printf("✅ Сообщение о блокировке %d элементов для %s%s отправлено (тип: %s)",
					len(ipsToBlock), entry.UserEmail, debugMarker, identifierType)
				p.enqueueSideEffectTask(func(ctx context.Context) {
					p.scheduleASNClear(ctx, entry.UserEmail)
				})
			}
		} else {
			log.Printf("⚠️  Все элементы для %s находятся в белом списке, блокировка не требуется", entry.UserEmail)
		}

		// Формируем алерт
		violationType := "asn_limit_exceeded"
		if identifierType == "Subnet" {
			violationType = "subnet_limit_exceeded_fallback"
		}

		alertPayload := models.AlertPayload{
			UserIdentifier: entry.UserEmail,
			Limit:          userASNLimit,
			BlockDuration:  p.cfg.BlockDuration,
			ViolationType:  violationType,
		}

		// Заполняем специфичные поля в зависимости от типа идентификатора
		if identifierType == "ASN" {
			// Для ASN режима: только ASN-специфичные поля
			asnCount := int(res.CurrentCount)
			alertPayload.DetectedASNCount = &asnCount
			alertPayload.AllUserASNs = res.AllUserItems
			// Передаём текущий ASN и IP для гарантированного включения в детали
			alertPayload.ASNDetails = p.collectASNDetails(ctx, entry.UserEmail, res.AllUserItems, identifier, entry.SourceIP)

			// Выполняем расширенную аналитику если включена
			if p.cfg.ScoringEnabled || p.cfg.GeoIPEnabled {
				geoResult, providerTypes, violationScore := p.performEnhancedAnalytics(
					ctx,
					entry.UserEmail,
					res.AllUserItems,
					alertPayload.ASNDetails,
				)

				// Добавляем результаты в alert payload
				if geoResult != nil {
					alertPayload.GeoAnalysis = geoResult
				}
				if providerTypes != nil {
					alertPayload.ProviderTypes = providerTypes
				}
				if violationScore != nil {
					score := violationScore.FinalScore
					alertPayload.Score = &score
					alertPayload.ScoreAction = string(violationScore.Action)

					// Проверяем действие на основе скора
					if violationScore.Action == scoring.ActionNone {
						log.Printf("[Anti-Abuse] Скор %.1f < 30 для %s, блокировка отменена",
							violationScore.FinalScore, entry.UserEmail)
						return // Не блокируем и не отправляем алерт
					}
				}
			}
		} else {
			// Для Subnet fallback: используем IP-поля
			subnetCount := int(res.CurrentCount)
			alertPayload.DetectedIPsCount = &subnetCount
			alertPayload.AllUserIPs = res.AllUserItems
		}

		p.enqueueSideEffectTask(func(ctx context.Context) {
			if err := p.alerter.SendAlert(ctx, alertPayload); err != nil {
				log.Printf("Ошибка отправки вебхук-уведомления: %v", err)
			}
		})
	}
}

// collectASNDetails собирает детали по каждому ASN (организация, IP, количество)
// currentASN и currentIP - текущий ASN и IP для гарантированного включения в результат
func (p *LogProcessor) collectASNDetails(ctx context.Context, email string, asns []string, currentASN, currentIP string) map[string]*models.ASNInfo {
	result := make(map[string]*models.ASNInfo)
	redisStore := p.storage.(*storage.RedisStore)

	for _, asn := range asns {
		// Пропускаем не-ASN идентификаторы (подсети)
		if len(asn) < 2 || asn[:2] != "AS" {
			continue
		}

		// Получаем IP-адреса для этого ASN
		ips, err := redisStore.GetIPsForUserASN(ctx, email, asn)
		if err != nil {
			log.Printf("Ошибка получения IP для ASN %s пользователя %s: %v", asn, email, err)
			ips = []string{} // Продолжаем с пустым списком вместо пропуска
		}

		// Если это текущий ASN и текущий IP не в списке - добавляем
		if asn == currentASN && currentIP != "" {
			found := false
			for _, ip := range ips {
				if ip == currentIP {
					found = true
					break
				}
			}
			if !found {
				ips = append(ips, currentIP)
			}
		}

		// Получаем название организации и страну для ASN
		// Сначала пробуем из кеша Redis
		org, err := redisStore.GetASNOrgName(ctx, asn)
		if err != nil {
			log.Printf("Ошибка получения org из кеша для ASN %s: %v", asn, err)
		}

		// Получаем код страны через lookup
		var countryCode string
		if p.asnLookup != nil && len(ips) > 0 {
			if org == "" {
				// Если в кеше нет org - используем LookupFull
				_, orgName, country, err := p.asnLookup.LookupFull(ips[0])
				if err == nil {
					if orgName != "" {
						org = orgName
						// Сохраняем в кеш для будущего использования
						if cacheErr := redisStore.SetASNOrgName(ctx, asn, orgName, p.cfg.UserSubnetTTL); cacheErr != nil {
							log.Printf("Ошибка кеширования org для ASN %s: %v", asn, cacheErr)
						}
					}
					countryCode = country
				}
			} else {
				// Если org уже есть - получаем только страну
				_, _, country, err := p.asnLookup.LookupFull(ips[0])
				if err == nil {
					countryCode = country
				}
			}
		}

		// Если org всё ещё пустой - ставим fallback
		if org == "" {
			org = "Unknown"
		}

		result[asn] = &models.ASNInfo{
			ASN:          asn,
			Organization: org,
			Country:      countryCode,
			IPs:          ips,
			IPCount:      len(ips),
		}
	}

	return result
}

// collectIPsForASNBlock собирает все IP-адреса для блокировки на основе ASN/подсетей
// currentIP - текущий IP для гарантированного включения в результат
func (p *LogProcessor) collectIPsForASNBlock(ctx context.Context, email string, identifiers []string, currentIP string) []string {
	var result []string
	seenIPs := make(map[string]struct{})
	redisStore := p.storage.(*storage.RedisStore)

	// Сначала добавляем текущий IP чтобы гарантировать его блокировку
	if currentIP != "" {
		seenIPs[currentIP] = struct{}{}
		result = append(result, currentIP)
	}

	for _, item := range identifiers {
		// Проверяем не в списке ли исключённых ASN
		if len(item) > 2 && item[:2] == "AS" {
			if p.cfg.ExcludedASNs[item] {
				log.Printf("ASN %s в списке исключённых, пропускаем при сборе IP для блокировки", item)
				continue
			}

			ips, err := redisStore.GetIPsForUserASN(ctx, email, item)
			if err != nil {
				log.Printf("Ошибка получения IP для ASN %s пользователя %s: %v", item, email, err)
				continue
			}
			for _, ip := range ips {
				if _, exists := seenIPs[ip]; !exists {
					seenIPs[ip] = struct{}{}
					result = append(result, ip)
				}
			}
		} else {
			// Это подсеть (fallback) - добавляем как есть для блокировки CIDR
			if _, exists := seenIPs[item]; !exists {
				seenIPs[item] = struct{}{}
				result = append(result, item)
			}
		}
	}

	if len(result) == 0 {
		log.Printf("⚠️  Не найдено IP-адресов для блокировки пользователя %s", email)
	}

	return result
}

// generateEventID возвращает 16-байтовый hex-идентификатор события из crypto/rand.
func generateEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure — крайне редкий случай, fallback на timestamp
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// publishBlockEvent разбивает список IP на чанки по cfg.MaxIPsPerBlockEvent и
// публикует каждый чанок как отдельное BlockMessage.  Если IP помещаются в один
// чанок — поля EventID/Chunk* не добавляются (wire-совместимость со старым форматом).
func (p *LogProcessor) publishBlockEvent(ips []string, duration string) error {
	chunkSize := p.cfg.MaxIPsPerBlockEvent
	if chunkSize <= 0 {
		chunkSize = 500
	}

	// Один чанок — старый формат без обёртки
	if len(ips) <= chunkSize {
		return p.publisher.PublishBlockMessage(models.BlockMessage{
			IPs:      ips,
			Duration: duration,
		})
	}

	// Несколько чанков — добавляем event envelope
	eventID := generateEventID()
	total := (len(ips) + chunkSize - 1) / chunkSize

	for i := 0; i < total; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(ips) {
			end = len(ips)
		}
		idx := i
		msg := models.BlockMessage{
			IPs:           ips[start:end],
			Duration:      duration,
			EventID:       eventID,
			ChunkIndex:    &idx,
			ChunkTotal:    &total,
			SchemaVersion: 2,
		}
		if err := p.publisher.PublishBlockMessage(msg); err != nil {
			return fmt.Errorf("chunk %d/%d (event %s): %w", i+1, total, eventID, err)
		}
	}
	return nil
}

// scheduleASNClear планирует отложенную очистку ASN данных
func (p *LogProcessor) scheduleASNClear(ctx context.Context, userEmail string) {
	log.Printf("Планирование отложенной очистки ASN данных для %s через %v.", userEmail, p.cfg.ClearIPsDelay)

	time.AfterFunc(p.cfg.ClearIPsDelay, func() {
		if ctx.Err() != nil {
			log.Printf("Отложенная очистка ASN данных для %s отменена из-за остановки сервиса.", userEmail)
			return
		}

		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		cleared, err := p.storage.(*storage.RedisStore).ClearUserASNData(opCtx, userEmail)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("Отложенная очистка ASN данных для %s отменена из-за остановки сервиса во время выполнения.", userEmail)
			} else if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Таймаут при отложенной очистке ASN данных для %s.", userEmail)
			} else {
				log.Printf("Ошибка при отложенной очистке ASN данных для %s: %v", userEmail, err)
			}
			return
		}
		log.Printf("✅ Отложенная очистка ASN данных для %s%s выполнена. Очищено ключей: %d",
			userEmail, p.getDebugMarker(userEmail), cleared)
	})
}

func (p *LogProcessor) scheduleIPsClear(ctx context.Context, userEmail string) {
	log.Printf("Планирование отложенной очистки IP для %s через %v.", userEmail, p.cfg.ClearIPsDelay)

	time.AfterFunc(p.cfg.ClearIPsDelay, func() {
		if ctx.Err() != nil {
			log.Printf("Отложенная очистка IP для %s отменена из-за остановки сервиса.", userEmail)
			return
		}

		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		cleared, err := p.storage.ClearUserIPs(opCtx, userEmail)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("Отложенная очистка IP для %s отменена из-за остановки сервиса во время выполнения.", userEmail)
			} else if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Таймаут при отложенной очистке IP для %s.", userEmail)
			} else {
				log.Printf("Ошибка при отложенной очистке IP для %s: %v", userEmail, err)
			}
			return
		}
		log.Printf("Отложенная очистка IP для %s%s выполнена. Очищено ключей: %d",
			userEmail, p.getDebugMarker(userEmail), cleared)
	})
}

func (p *LogProcessor) scheduleSubnetsClear(ctx context.Context, userEmail string) {
	log.Printf("Планирование отложенной очистки ПОДСЕТЕЙ для %s через %v.", userEmail, p.cfg.ClearIPsDelay)

	time.AfterFunc(p.cfg.ClearIPsDelay, func() {
		if ctx.Err() != nil {
			log.Printf("Отложенная очистка ПОДСЕТЕЙ для %s отменена из-за остановки сервиса.", userEmail)
			return
		}

		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		cleared, err := p.storage.ClearUserSubnets(opCtx, userEmail)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("Отложенная очистка ПОДСЕТЕЙ для %s отменена из-за остановки сервиса во время выполнения.", userEmail)
			} else if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Таймаут при отложенной очистке ПОДСЕТЕЙ для %s.", userEmail)
			} else {
				log.Printf("Ошибка при отложенной очистке ПОДСЕТЕЙ для %s: %v", userEmail, err)
			}
			return
		}
		log.Printf("Отложенная очистка ПОДСЕТЕЙ для %s%s выполнена. Очищено ключей: %d",
			userEmail, p.getDebugMarker(userEmail), cleared)
	})
}

// performEnhancedAnalytics выполняет расширенную аналитику с GeoIP и скорингом
func (p *LogProcessor) performEnhancedAnalytics(
	ctx context.Context,
	email string,
	allASNs []string,
	asnDetails map[string]*models.ASNInfo,
) (
	*models.GeoAnalysisResult,
	map[string]string,
	*scoring.ViolationScore,
) {
	// Если сервисы не настроены - возвращаем nil
	if p.geoService == nil || p.geoAnalyzer == nil || p.asnClassifier == nil || p.scorer == nil {
		return nil, nil, nil
	}

	// 1. Собираем все IP-адреса пользователя
	allIPs := make([]string, 0)
	for _, info := range asnDetails {
		allIPs = append(allIPs, info.IPs...)
	}

	// 2. Выполняем географический анализ
	geoResultInternal := p.geoAnalyzer.AnalyzeUserIPs(ctx, allIPs)

	// Конвертируем в models.GeoAnalysisResult
	geoResult := &models.GeoAnalysisResult{
		UniqueCountries: geoResultInternal.UniqueCountries,
		UniqueCities:    geoResultInternal.UniqueCities,
		Agglomerations:  geoResultInternal.Agglomerations,
		MaxDistanceKM:   geoResultInternal.MaxDistanceKM,
		GeoScore:        geoResultInternal.GeoScore,
		GeoFlags:        geoResultInternal.GeoFlags,
	}

	// 3. Классифицируем провайдеров и обогащаем ASNInfo
	asnClassifications := make(map[string]*asn.ASNClassification)
	providerTypes := make(map[string]string)

	for asnStr, info := range asnDetails {
		// Используем ClassifyWithCountry для более точной классификации
		classification := p.asnClassifier.ClassifyWithCountry(asnStr, info.Organization, info.Country)
		asnClassifications[asnStr] = classification
		providerTypes[asnStr] = classification.ProviderType

		// Обогащаем ASNInfo
		info.ProviderType = classification.ProviderType
		info.Modifier = classification.Modifier
	}

	// 4. Рассчитываем скор (конвертируем обратно в geoip.GeoAnalysisResult для scorer)
	geoResultForScorer := &geoip.GeoAnalysisResult{
		UniqueCountries: geoResult.UniqueCountries,
		UniqueCities:    geoResult.UniqueCities,
		Agglomerations:  geoResult.Agglomerations,
		MaxDistanceKM:   geoResult.MaxDistanceKM,
		GeoScore:        geoResult.GeoScore,
		GeoFlags:        geoResult.GeoFlags,
	}

	violationScore := p.scorer.Calculate(
		asnClassifications,
		geoResultForScorer,
		len(allASNs),
		p.cfg.MaxASNsPerUser,
	)

	log.Printf("[Anti-Abuse] Анализ для %s: GeoScore=%d, ASNScore=%.1f, FinalScore=%.1f, Action=%s",
		email, geoResult.GeoScore, violationScore.Components.ASNScore,
		violationScore.FinalScore, violationScore.Action)

	return geoResult, providerTypes, violationScore
}
