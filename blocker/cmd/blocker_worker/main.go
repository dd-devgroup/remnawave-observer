package main

import (
	"blocker-worker/internal/config"
	"blocker-worker/internal/logger"
	"blocker-worker/internal/processor"
	"blocker-worker/internal/services/command"
	"blocker-worker/internal/worker"
	"fmt"
)

func main() {
	// 1. Инициализация зависимостей
	l := logger.New()
	cfg := config.New()
	cmdExecutor := command.NewExecutor(l)

	// 2. Создание worker pool
	pool := processor.NewWorkerPool(cfg.BlockerWorkers, cfg.QueueSize, cfg.DrainTimeout)
	pool.Start()
	l.Info(fmt.Sprintf("Worker pool запущен: %d воркеров, очередь %d", cfg.BlockerWorkers, cfg.QueueSize))

	// 3. Инициализация процессора с pool и nftTimeout
	msgProcessor := processor.NewMessageProcessor(l, cmdExecutor, pool, cfg.NftTimeout)

	// 4. Инициализация главного воркера
	appWorker := worker.New(l, cfg, msgProcessor, pool)

	// 5. Запуск приложения
	appWorker.Run()
}