package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/publisher"
	"observer_service/internal/services/storage"
	"time"

	"github.com/gin-gonic/gin"
)

// EntryEnqueuer определяет интерфейс для добавления записей в очередь обработки.
type EntryEnqueuer interface {
	EnqueueEntries(entries []models.LogEntry) error
}

type Server struct {
	router    *gin.Engine
	enqueuer  EntryEnqueuer
	storage   storage.IPStorage
	publisher publisher.EventPublisher
	port      string
	cfg       *config.Config
}

func NewServer(port string, enqueuer EntryEnqueuer, storage storage.IPStorage, pub publisher.EventPublisher, cfg *config.Config) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	s := &Server{
		router:    router,
		enqueuer:  enqueuer,
		storage:   storage,
		publisher: pub,
		port:      port,
		cfg:       cfg,
	}

	s.setupRoutes()
	return s
}

func (s *Server) GetRouter() *gin.Engine {
	return s.router
}

func (s *Server) setupRoutes() {
	s.router.POST("/log-entry", s.handleProcessLogEntries)
	s.router.GET("/health", s.handleHealthCheck)
}

func (s *Server) Run() error {
	return s.router.Run(":" + s.port)
}

func (s *Server) handleProcessLogEntries(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.cfg.MaxRequestBytes)

	var entries []models.LogEntry
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body exceeds maximum allowed size", "code": "body_too_large"})
			return
		}
		if errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "empty request body", "code": "invalid_json"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "invalid_json"})
		return
	}

	if len(entries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty entries array", "code": "empty_entries"})
		return
	}

	if len(entries) > s.cfg.MaxLogEntriesPerRequest {
		log.Printf("Отклонён запрос: %d записей, максимум %d", len(entries), s.cfg.MaxLogEntriesPerRequest)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("too many entries: %d, max allowed: %d", len(entries), s.cfg.MaxLogEntriesPerRequest),
			"code":  "too_many_entries",
		})
		return
	}

	for i, entry := range entries {
		if entry.UserEmail == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("missing user_email at index %d", i),
				"code":  "invalid_json",
			})
			return
		}
		if _, err := netip.ParseAddr(entry.SourceIP); err != nil {
			log.Printf("Невалидный source_ip в записи %d для пользователя %s: %q", i, entry.UserEmail, entry.SourceIP)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("invalid source_ip at index %d: %q", i, entry.SourceIP),
				"code":  "invalid_ip",
			})
			return
		}
	}

	if err := s.enqueuer.EnqueueEntries(entries); err != nil {
		log.Printf("Warning: log queue is full. Rejecting request for %d entries. Error: %v", len(entries), err)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Service is temporarily overloaded. Please try again later.",
			"code":  "service_overloaded",
		})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"status":            "accepted",
		"processed_entries": len(entries),
	})
}

func (s *Server) handleHealthCheck(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status := http.StatusOK
	response := gin.H{
		"redis_connection":    "ok",
		"rabbitmq_connection": "ok",
	}

	if err := s.storage.Ping(ctx); err != nil {
		status = http.StatusServiceUnavailable
		response["redis_connection"] = "failed"
	}

	if err := s.publisher.Ping(); err != nil {
		status = http.StatusServiceUnavailable
		response["rabbitmq_connection"] = "failed"
	}

	if status == http.StatusOK {
		response["status"] = "ok"
	} else {
		response["status"] = "error"
	}

	c.JSON(status, response)
}
