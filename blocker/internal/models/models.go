package models

// BlockingPayload представляет структуру входящих сообщений из RabbitMQ.
// Поля EventID, ChunkIndex, ChunkTotal, SchemaVersion добавлены для совместимости
// с новым форматом Observer (omitempty — они опциональны).
type BlockingPayload struct {
	IPs      []string `json:"ips"`
	Duration string   `json:"duration"`

	// Опциональные поля для chunked событий (Observer Phase 1, PR8)
	EventID      string `json:"event_id,omitempty"`
	ChunkIndex   int    `json:"chunk_index,omitempty"`
	ChunkTotal   int    `json:"chunk_total,omitempty"`
	SchemaVersion int   `json:"schema_version,omitempty"`
}