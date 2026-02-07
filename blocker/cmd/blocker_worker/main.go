package main

import (
	"blocker-worker/internal/config"
	"blocker-worker/internal/logger"
	"blocker-worker/internal/metrics"
	"blocker-worker/internal/processor"
	"blocker-worker/internal/services/command"
	"blocker-worker/internal/worker"
	"fmt"
	"time"
)

func main() {
	// 1. Инициализация зависимостей
	l := logger.New()
	cfg := config.New()
	cmdExecutor := command.NewExecutor(l)

	// 2. Запуск metrics dumper (каждые 60 секунд)
	metrics.StartDumper(l, 60*time.Second)
	l.Info("Metrics dumper запущен (интервал: 60s)")

	// 3. Создание worker pool
	pool := processor.NewWorkerPool(cfg.BlockerWorkers, cfg.QueueSize, cfg.DrainTimeout)
	pool.Start()
	l.Info(fmt.Sprintf("Worker pool запущен: %d воркеров, очередь %d", cfg.BlockerWorkers, cfg.QueueSize))

	// 4. Инициализация процессора с pool и nftTimeout
	msgProcessor := processor.NewMessageProcessor(l, cmdExecutor, pool, cfg.NftTimeout)

	// 5. Инициализация главного воркера
	appWorker := worker.New(l, cfg, msgProcessor, pool)

	// 6. Запуск приложения
	appWorker.Run()
}