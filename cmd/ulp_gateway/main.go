package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"slp/pkg/ulpgw"
)

func main() {
	grpcAddr := flag.String("grpc", ":7276", "gRPC listen address for SLP server bidirectional stream")
	ueAddr := flag.String("ue", ":7275", "TCP/TLS listen address for SUPL Enabled Terminals (UE)")
	flag.Parse()

	log.Println("=================================================================")
	log.Println("           ULP Gateway Server (TLS/TCP edge to gRPC)             ")
	log.Println("=================================================================")

	server := ulpgw.NewUlpGatewayServer()
	if err := server.Start(*grpcAddr, *ueAddr); err != nil {
		log.Fatalf("Failed to start ULP Gateway: %v", err)
	}
	defer server.Stop()

	log.Printf("[ULPGW] gRPC Service listening on %s (for SLP connections)", server.GrpcAddr())
	log.Printf("[ULPGW] Terminal Edge listening on %s (for UE TLS/TCP connections)", server.UeAddr())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down ULP Gateway...", sig)
	fmt.Println("ULP Gateway stopped.")
}
