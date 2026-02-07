package processor

import (
	"blocker-worker/internal/logger"
	"blocker-worker/internal/models"
	"blocker-worker/internal/services/command"
	"blocker-worker/internal/validation"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

var validDurationPattern = regexp.MustCompile(`^\d+[smhd]$`)

// MessageProcessor инкапсулирует логику обработки одного сообщения RabbitMQ.
type MessageProcessor struct {
	logger     *logger.Logger
	executor   *command.Executor
	pool       *WorkerPool
	nftTimeout time.Duration
}

// NewMessageProcessor создает новый обработчик сообщений.
func NewMessageProcessor(l *logger.Logger, exec *command.Executor, pool *WorkerPool, nftTimeout time.Duration) *MessageProcessor {
	return &MessageProcessor{
		logger:     l,
		executor:   exec,
		pool:       pool,
		nftTimeout: nftTimeout,
	}
}

// logEventContext формирует контекстную строку для логов с EventID/ChunkIndex если они есть.
func logEventContext(payload *models.BlockingPayload) string {
	if payload.EventID != "" {
		if payload.ChunkTotal > 1 {
			return fmt.Sprintf("[EventID=%s, Chunk=%d/%d]", payload.EventID, payload.ChunkIndex+1, payload.ChunkTotal)
		}
		return fmt.Sprintf("[EventID=%s]", payload.EventID)
	}
	return ""
}

// Process принимает тело сообщения и выполняет действие по блокировке.
func (p *MessageProcessor) Process(ctx context.Context, body []byte) error {
	var payload models.BlockingPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		p.logger.Error(fmt.Sprintf("Не удалось декодировать JSON из сообщения: %s", string(body)))
		// Невалидный JSON — ack сообщение, чтобы не зациклить очередь
		return err
	}

	eventCtx := logEventContext(&payload)

	if len(payload.IPs) == 0 {
		p.logger.Info(fmt.Sprintf("Сообщение не содержит IP-адресов для блокировки, пропускаем. %s", eventCtx))
		return nil
	}

	duration := payload.Duration
	if duration == "" {
		duration = "5m"
	}

	if !validDurationPattern.MatchString(duration) {
		err := fmt.Errorf("недопустимый формат duration, сообщение отклонено: '%s' %s", duration, eventCtx)
		p.logger.Error(err.Error())
		// Невалидный duration — ack сообщение (poison message)
		return err
	}

	// Валидируем все IP до начала обработки
	validIPs := make([]string, 0, len(payload.IPs))
	for _, ip := range payload.IPs {
		result := validation.ValidateIPOrCIDR(ip)
		if !result.Valid {
			p.logger.Warning(fmt.Sprintf("Невалидный IP/CIDR пропущен: %s (причина: %s) %s", ip, result.Error, eventCtx))
			continue
		}
		validIPs = append(validIPs, ip)
	}

	if len(validIPs) == 0 {
		p.logger.Warning(fmt.Sprintf("Все IP/CIDR в сообщении невалидны, нечего блокировать. %s", eventCtx))
		// Ack сообщение — это poison message с неправильными данными
		return nil
	}

	p.logger.Info(fmt.Sprintf("Обработка %d валидных IP/CIDR из %d. %s", len(validIPs), len(payload.IPs), eventCtx))

	// Отправляем задачи в worker pool вместо создания неограниченных goroutines
	var wg sync.WaitGroup
	for _, ip := range validIPs {
		wg.Add(1)
		ipAddress := ip // Захватываем переменную для closure

		task := func(taskCtx context.Context) {
			defer wg.Done()

			// Создаем child context с timeout для конкретной nft операции
			nftCtx, cancel := context.WithTimeout(taskCtx, p.nftTimeout)
			defer cancel()

			// Используем безопасную функцию формирования set expression
			setExpr := command.BuildSetExpression(ipAddress, duration)
			err := p.executor.RunNftCommand(nftCtx, "add", "element", "inet", "firewall", "user_blacklist", setExpr)
			if err != nil {
				// Проверяем, была ли это timeout ошибка
				if errors.Is(err, context.DeadlineExceeded) {
					p.logger.Error(fmt.Sprintf("TIMEOUT при обработке IP %s (превышен лимит %v). %s", ipAddress, p.nftTimeout, eventCtx))
				} else if errors.Is(err, context.Canceled) {
					p.logger.Warning(fmt.Sprintf("Операция отменена для IP %s (shutdown). %s", ipAddress, eventCtx))
				} else {
					p.logger.Error(fmt.Sprintf("Ошибка при обработке IP %s: %v %s", ipAddress, err, eventCtx))
				}
			}
		}

		// Пытаемся отправить задачу в pool
		if !p.pool.Submit(task) {
			// Pool остановлен (shutdown) — уменьшаем счетчик и прерываем
			wg.Done()
			p.logger.Warning(fmt.Sprintf("Worker pool остановлен, прерываем обработку. %s", eventCtx))
			break
		}

		// Проверяем, не заполнена ли очередь (для логирования backpressure)
		if p.pool.QueueLen() > p.pool.QueueCap()*9/10 {
			p.logger.Warning(fmt.Sprintf("Очередь worker pool почти заполнена: %d/%d. %s", p.pool.QueueLen(), p.pool.QueueCap(), eventCtx))
		}
	}

	wg.Wait()
	return nil
}