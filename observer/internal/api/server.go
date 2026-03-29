package api

import (
	"context"
	"net/http"
	"observer_service/internal/config"
	"observer_service/internal/metrics"
	"observer_service/internal/services/storage"
	"time"

	"github.com/gin-gonic/gin"
)

type Server struct {
	router  *gin.Engine
	storage storage.Storage
	port    string
	cfg     *config.Config
}

func NewServer(port string, storage storage.Storage, cfg *config.Config) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	s := &Server{
		router:  router,
		storage: storage,
		port:    port,
		cfg:     cfg,
	}

	s.setupRoutes()
	return s
}

func (s *Server) GetRouter() *gin.Engine {
	return s.router
}

func (s *Server) setupRoutes() {
	s.router.POST("/log-entry", s.handleLegacyLogIngestRemoved)
	s.router.GET("/health", s.handleHealthCheck)
}

func (s *Server) Run() error {
	return s.router.Run(":" + s.port)
}

func (s *Server) handleLegacyLogIngestRemoved(c *gin.Context) {
	metrics.RequestsTotal.Add(1)
	metrics.RejectedRequestsTotal.Add(1)
	c.JSON(http.StatusGone, gin.H{
		"error": "legacy HTTP ingest has been removed; observer is panel-only",
		"code":  "legacy_ingest_removed",
	})
}

func (s *Server) handleHealthCheck(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status := http.StatusOK
	response := gin.H{
		"redis_connection": "ok",
	}

	if err := s.storage.Ping(ctx); err != nil {
		status = http.StatusServiceUnavailable
		response["redis_connection"] = "failed"
	}

	if status == http.StatusOK {
		response["status"] = "ok"
	} else {
		response["status"] = "error"
	}

	c.JSON(status, response)
}
