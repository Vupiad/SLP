package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"slp/pkg/bearer"
	"slp/pkg/metrics"
	"slp/pkg/nims"
	"slp/pkg/posinit"
	"slp/pkg/server"
	"slp/pkg/session"
	"slp/pkg/smspush"
	"slp/pkg/ulpgw"
)

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP REST API listen address (Ngmlc API & Metrics)")
	smscAddr := flag.String("smsc", "127.0.0.1:2775", "SMSC SMPP server address (host:port)")
	gwAddr := flag.String("ulpgw", "127.0.0.1:7276", "ULP Gateway gRPC server address (host:port)")
	slpFqdn := flag.String("fqdn", "slp.viettel.vn", "SLP Fully Qualified Domain Name")
	embedSmsc := flag.Bool("embed-smsc", false, "Start embedded mock SMSC for local testing")
	flag.Parse()

	log.Println("=================================================================")
	log.Println("   Combined GMLC/SLP 5G Positioning Platform (SUPL 2.0 / 5GC)   ")
	log.Println("=================================================================")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Optional embedded mock SMSC for zero-dependency standalone execution
	if *embedSmsc {
		mockSmsc, err := smspush.StartMockSMSC(*smscAddr)
		if err != nil {
			log.Fatalf("Failed to start embedded SMSC: %v", err)
		}
		defer mockSmsc.Stop()
		*smscAddr = mockSmsc.Addr()
		log.Printf("[SMSC] Embedded Mock SMSC listening on %s", *smscAddr)
	}

	// 2. Initialize Session Store
	sessionStore := session.NewInMemorySessionStore(1 * time.Second)
	defer sessionStore.Close()
	log.Println("[Store] Session Manager initialized with active TTL cleanup")

	// 3. Initialize NIMS Cell-ID database
	nimsDb := nims.NewDatabase()
	log.Println("[NIMS] Network Information Management System database loaded")

	// 4. Initialize ULP Gateway gRPC Client with exponential backoff & keepalive
	gwCfg := ulpgw.ClientConfig{
		GatewayAddr: *gwAddr,
		Backoff:     ulpgw.DefaultBackoffConfig(),
		Keepalive:   ulpgw.DefaultKeepaliveConfig(),
	}
	gwClient := ulpgw.NewUlpGatewayClient(gwCfg)
	gwClient.Start(ctx)
	defer gwClient.Close()
	log.Printf("[ULPGW] ULP Gateway client started (target: %s)", *gwAddr)

	// 5. Initialize SUPL POS INIT processor
	processor := posinit.NewProcessor(sessionStore, nimsDb, gwClient)
	log.Println("[Processor] SUPL POS INIT processor registered with Gateway client")

	// 6. Initialize SMPP Client for SMS transport
	smppCfg := smspush.SMPPConfig{
		Addr:        *smscAddr,
		SystemID:    "SLP_APP",
		Password:    "SECRET",
		SystemType:  "SUPL",
		SourceAddr:  "1900",
		DialTimeout: 3 * time.Second,
		ReadTimeout: 3 * time.Second,
	}
	smppClient := smspush.NewSMPPClient(smppCfg)
	if err := smppClient.Connect(ctx); err != nil {
		log.Printf("[SMPP] Warning: Could not connect to SMSC %s: %v (will retry on demand)", *smscAddr, err)
	} else {
		log.Printf("[SMPP] Connected and bound as transmitter to SMSC at %s", *smscAddr)
	}
	defer smppClient.Close()

	// 7. Initialize Bearer Selection & Fallback Engine
	counters := metrics.DefaultCounters
	defaultAuthKey := []byte("secret-viettel-auth-key-2026")
	upRunner := bearer.NewUPRunner(sessionStore, smppClient, processor, defaultAuthKey, *slpFqdn, counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)
	log.Println("[Bearer] Combined GMLC/SLP Bearer Selector initialized (UP <-> CP fallback enabled)")

	// 8. Start HTTP REST Server
	srv := server.NewServer(selector, counters)
	if err := srv.Start(*httpAddr); err != nil {
		log.Fatalf("Failed to start HTTP server on %s: %v", *httpAddr, err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
	}()

	log.Printf("[HTTP] Ngmlc ProvideLocation API & Metrics listening on http://%s", srv.Addr())
	log.Printf("[HTTP] Endpoints:")
	log.Printf("       - POST http://%s/ngmlc/v1/provide-location", srv.Addr())
	log.Printf("       - GET  http://%s/metrics", srv.Addr())
	log.Printf("       - GET  http://%s/health", srv.Addr())

	// Wait for OS termination signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down gracefully...", sig)
	fmt.Println("SLP stopped cleanly.")
}
