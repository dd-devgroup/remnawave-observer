package processor

import (
	"context"
	"log"
	"observer_service/internal/database"
	"sync"
	"time"
)

const (
	batchSize     = 100
	flushInterval = 5 * time.Second
)

// BatchWriter buffers UserConnection records and flushes them to the database
// in batches, either when the batch reaches batchSize or on a timer tick.
type BatchWriter struct {
	repo   database.Repository
	ch     chan database.UserConnection
	ctx    context.Context
	cancel context.CancelFunc
}

// NewBatchWriter creates a new BatchWriter.
func NewBatchWriter(repo database.Repository) *BatchWriter {
	ctx, cancel := context.WithCancel(context.Background())
	return &BatchWriter{
		repo:   repo,
		ch:     make(chan database.UserConnection, batchSize*2),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Write enqueues a UserConnection for batch writing. Non-blocking; drops if full.
func (w *BatchWriter) Write(conn database.UserConnection) {
	select {
	case w.ch <- conn:
	default:
		log.Println("Warning: batch writer queue full, dropping connection record")
	}
}

// Run starts the flush loop. Call from a goroutine. Blocks until ctx is cancelled.
func (w *BatchWriter) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("BatchWriter started")

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	var batch []database.UserConnection

	for {
		select {
		case conn := <-w.ch:
			batch = append(batch, conn)
			if len(batch) >= batchSize {
				w.flush(ctx, &batch)
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.flush(ctx, &batch)
			}
		case <-ctx.Done():
			// Drain remaining items
			close(w.ch)
			for conn := range w.ch {
				batch = append(batch, conn)
			}
			if len(batch) > 0 {
				// Use background context for final flush
				w.flush(context.Background(), &batch)
			}
			log.Println("BatchWriter stopped")
			return
		}
	}
}

func (w *BatchWriter) flush(ctx context.Context, batch *[]database.UserConnection) {
	if len(*batch) == 0 {
		return
	}

	if err := w.repo.InsertConnections(ctx, *batch); err != nil {
		log.Printf("BatchWriter flush error (%d records): %v", len(*batch), err)
		// Records are lost on error — acceptable for connection logs
	}

	*batch = (*batch)[:0]
}
