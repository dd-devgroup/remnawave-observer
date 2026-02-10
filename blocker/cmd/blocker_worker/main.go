package main

import (
	"blocker-worker/internal/config"
	"blocker-worker/internal/firewall"
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

	// 4. Создание firewall backend (exec по умолчанию)
	fwBackend := firewall.NewExecBackend(l, cmdExecutor)
	l.Info(fmt.Sprintf("Firewall backend: %s", fwBackend.Name()))

	// 5. Инициализация процессора с pool и nftTimeout
	msgProcessor := processor.NewMessageProcessor(l, fwBackend, pool, cfg.NftTimeout)

	// 6. Инициализация главного воркера
	appWorker := worker.New(l, cfg, msgProcessor, pool)

	// 7. Запуск приложения
	appWorker.Run()
}