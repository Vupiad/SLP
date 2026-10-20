package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"slp/pkg/bearer"
	"slp/pkg/metrics"
	"slp/pkg/nims"
	"slp/pkg/posinit"
	"slp/pkg/server"
	"slp/pkg/session"
)

func TestServerApiEndpoints(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	upRunner := bearer.NewUPRunner(store, nil, proc, []byte("key"), "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	srv := server.NewServer(selector, counters)
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer srv.Stop(context.Background())

	baseURL := "http://" + srv.Addr()

	// 1. Test GET /health
	respHealth, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	if respHealth.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respHealth.StatusCode)
	}
	_ = respHealth.Body.Close()

	// 2. Test GET /metrics
	respMetrics, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	bodyMetrics, _ := io.ReadAll(respMetrics.Body)
	_ = respMetrics.Body.Close()
	if !bytes.Contains(bodyMetrics, []byte("slp_requests_total")) {
		t.Fatalf("expected slp_requests_total in metrics output:\n%s", string(bodyMetrics))
	}

	// 3. Test POST /ngmlc/v1/provide-location (CP_ONLY mode for immediate synchronous test)
	reqPayload := server.ProvideLocationRequest{
		Target:      "+84988111222",
		ServiceType: "emergency",
		Bearer:      "CP_ONLY",
		TimeoutMs:   2000,
	}
	jsonBytes, _ := json.Marshal(reqPayload)

	postResp, err := http.Post(baseURL+"/ngmlc/v1/provide-location", "application/json", bytes.NewReader(jsonBytes))
	if err != nil {
		t.Fatalf("POST provide-location failed: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", postResp.StatusCode)
	}

	var provResp server.ProvideLocationResponse
	if err := json.NewDecoder(postResp.Body).Decode(&provResp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if provResp.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s: %s", provResp.Status, provResp.Error)
	}
	if provResp.BearerUsed != "CONTROL_PLANE" {
		t.Fatalf("expected CONTROL_PLANE, got %s", provResp.BearerUsed)
	}
	if provResp.Location == nil || provResp.Location.Latitude != 21.013589 {
		t.Fatalf("location coordinates mismatch: %+v", provResp.Location)
	}
}

func TestServerValidationAndErrorBranches(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	upRunner := bearer.NewUPRunner(store, nil, proc, []byte("key"), "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	cpRunner.SetSimulateFailure(true) // will cause CP_ONLY to fail
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	srv := server.NewServer(selector, counters)
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer srv.Stop(context.Background())

	baseURL := "http://" + srv.Addr()

	// 1. Invalid JSON body
	respBadJson, err := http.Post(baseURL+"/ngmlc/v1/provide-location", "application/json", bytes.NewReader([]byte("not-valid-json")))
	if err != nil || respBadJson.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid json, got %v", respBadJson.StatusCode)
	}
	_ = respBadJson.Body.Close()

	// 2. Missing target
	emptyTargetReq := `{"target":""}`
	respEmptyTarget, err := http.Post(baseURL+"/ngmlc/v1/provide-location", "application/json", bytes.NewReader([]byte(emptyTargetReq)))
	if err != nil || respEmptyTarget.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty target, got %v", respEmptyTarget.StatusCode)
	}
	_ = respEmptyTarget.Body.Close()

	// 3. Request that fails positioning (CP_ONLY with simulateFailure=true)
	horacc := int64(100)
	veracc := int64(50)
	maxAge := int64(30)
	delay := int64(5)
	failReq := server.ProvideLocationRequest{
		Target:    "+84988000999",
		Bearer:    "CP_ONLY",
		TimeoutMs: 1000,
		QoP: &server.ProvideLocQoP{
			Horacc:    &horacc,
			Veracc:    &veracc,
			MaxLocAge: &maxAge,
			Delay:     &delay,
		},
	}
	failJson, _ := json.Marshal(failReq)
	respFail, err := http.Post(baseURL+"/ngmlc/v1/provide-location", "application/json", bytes.NewReader(failJson))
	if err != nil {
		t.Fatalf("http post error: %v", err)
	}
	defer respFail.Body.Close()

	if respFail.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 Gateway Timeout, got %d", respFail.StatusCode)
	}
	var failResp server.ProvideLocationResponse
	_ = json.NewDecoder(respFail.Body).Decode(&failResp)
	if failResp.Status != "FAILED" {
		t.Fatalf("expected status FAILED, got %s", failResp.Status)
	}
}
