package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"slp/pkg/bearer"
	"slp/pkg/metrics"
	"slp/pkg/session"
)

// ServerConfig defines HTTP server configuration
type ServerConfig struct {
	ListenAddr string
}

// ProvideLocationRequest represents the HTTP request payload
type ProvideLocationRequest struct {
	Target      string           `json:"target"`
	ServiceType string           `json:"serviceType,omitempty"`
	Bearer      string           `json:"bearer,omitempty"` // UP_ONLY, CP_ONLY, UP_PREFERRED, CP_PREFERRED
	QoP         *ProvideLocQoP   `json:"qop,omitempty"`
	TimeoutMs   int64            `json:"timeoutMs,omitempty"`
}

// ProvideLocQoP represents QoP JSON parameters
type ProvideLocQoP struct {
	Horacc    *int64 `json:"horacc,omitempty"`
	Veracc    *int64 `json:"veracc,omitempty"`
	MaxLocAge *int64 `json:"maxLocAge,omitempty"`
	Delay     *int64 `json:"delay,omitempty"`
}

// ProvideLocationResponse represents the HTTP response payload
type ProvideLocationResponse struct {
	Status           string                  `json:"status"`
	BearerUsed       string                  `json:"bearerUsed,omitempty"`
	FallbackOccurred bool                    `json:"fallbackOccurred"`
	Location         *session.LocationResult `json:"location,omitempty"`
	DurationMs       int64                   `json:"durationMs"`
	Error            string                  `json:"error,omitempty"`
}

// Server provides the HTTP REST API for Combined GMLC/SLP
type Server struct {
	selector *bearer.Selector
	counters *metrics.TelemetryCounters
	listener net.Listener
	httpSrv  *http.Server
	addr     string
}

// NewServer initializes a new Combined GMLC/SLP API server
func NewServer(sel *bearer.Selector, counters *metrics.TelemetryCounters) *Server {
	if counters == nil {
		counters = metrics.DefaultCounters
	}
	s := &Server{
		selector: sel,
		counters: counters,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ngmlc/v1/provide-location", s.handleProvideLocation)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /health", s.handleHealth)

	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	return s
}

// Start runs the HTTP server on listenAddr
func (s *Server) Start(listenAddr string) error {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}
	s.listener = l
	s.addr = l.Addr().String()

	go func() {
		_ = s.httpSrv.Serve(l)
	}()
	return nil
}

// Addr returns the listening address of the server
func (s *Server) Addr() string {
	return s.addr
}

func (s *Server) handleProvideLocation(w http.ResponseWriter, r *http.Request) {
	var req ProvideLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"status":"FAILED","error":"invalid json: %v"}`, err), http.StatusBadRequest)
		return
	}

	if req.Target == "" {
		http.Error(w, `{"status":"FAILED","error":"target is required"}`, http.StatusBadRequest)
		return
	}

	var qop session.QoP
	if req.QoP != nil {
		qop.Horacc = req.QoP.Horacc
		qop.Veracc = req.QoP.Veracc
		qop.MaxLocAge = req.QoP.MaxLocAge
		qop.Delay = req.QoP.Delay
	}

	timeout := 10 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	locReq := bearer.LocationRequest{
		TargetID: req.Target,
		Bearer:   bearer.BearerMode(req.Bearer),
		QoP:      qop,
		Timeout:  timeout,
	}

	locResp := s.selector.ProvideLocation(r.Context(), locReq)

	resp := ProvideLocationResponse{
		Status:           locResp.Status,
		BearerUsed:       string(locResp.BearerUsed),
		FallbackOccurred: locResp.FallbackOccurred,
		Location:         locResp.Location,
		DurationMs:       locResp.Duration.Milliseconds(),
		Error:            locResp.ErrorMsg,
	}

	w.Header().Set("Content-Type", "application/json")
	if locResp.Status != "SUCCESS" {
		w.WriteHeader(http.StatusGatewayTimeout)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(s.counters.PrometheusFormat()))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	snap := s.counters.Snapshot()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "UP",
		"service": "Combined-GMLC-SLP",
		"metrics": snap,
	})
}

// Stop shuts down the HTTP server
func (s *Server) Stop(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
