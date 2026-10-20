package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"slp/pkg/oma_ulp"
	"slp/pkg/smspush"
	"slp/pkg/uper"
)

func ptr[T any](v T) *T {
	return &v
}

func main() {
	gatewayAddr := flag.String("gw", "127.0.0.1:7275", "ULP Gateway UE edge address (host:port)")
	msisdn := flag.String("msisdn", "+84988000999", "Terminal MSISDN")
	authKeyHex := flag.String("key", "7365637265742d7669657474656c2d617574682d6b65792d32303236", "Shared auth key in hex")
	flag.Parse()

	log.Println("=================================================================")
	log.Println("          Mock SUPL Enabled Terminal (SET / UE)                 ")
	log.Println("=================================================================")
	log.Printf("Target MSISDN: %s", *msisdn)
	log.Printf("ULP Gateway:   %s", *gatewayAddr)

	_ = authKeyHex

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	log.Println("Mock UE ready. Press Ctrl+C to terminate.")
	<-sigCh
	fmt.Println("Mock UE stopped.")
}

func sendSuplPosInit(conn net.Conn, slpSessID []byte, authKey []byte, rawSuplInit []byte) error {
	terminalVer := oma_ulp.ComputeVer(authKey, rawSuplInit)

	posInitPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SetSessionID: &oma_ulp.SetSessionID{
				SessionId: 1234,
				SetId: oma_ulp.SETId{
					Choice: oma_ulp.SETIdChoice_Msisdn,
					Msisdn: ptr([]byte{0x84, 0x98, 0x80, 0x00, 0x99, 0x90, 0x00, 0x00}),
				},
			},
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: slpSessID,
				SlpId: oma_ulp.SLPAddress{
					Choice: oma_ulp.SLPAddressChoice_Fqdn,
					Fqdn:   ptr(oma_ulp.FQDN("slp.viettel.vn")),
				},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
			MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
				LocationId: oma_ulp.LocationId{
					Status: oma_ulp.Status_Current,
					CellInfo: oma_ulp.CellInfo{
						Choice: oma_ulp.CellInfoChoice_GsmCell,
						GsmCell: &oma_ulp.GsmCellInformation{
							RefMCC: 452,
							RefMNC: 4,
							RefLAC: 100,
							RefCI:  500,
						},
					},
				},
				Ver: terminalVer,
			},
		},
	}

	encoded, err := oma_ulp.Marshal(&posInitPdu)
	if err != nil {
		return err
	}

	// Send over TCP with 2-byte length prefix
	lenBuf := make([]byte, 2)
	lenBuf[0] = byte(len(encoded) >> 8)
	lenBuf[1] = byte(len(encoded) & 0xFF)
	if _, err := conn.Write(lenBuf); err != nil {
		return err
	}
	_, err = conn.Write(encoded)
	return err
}

func handleWapPushSms(data []byte, authKey []byte, gwAddr string) {
	rawSuplInit, err := smspush.ExtractSuplInitFromSmsUserData(data)
	if err != nil {
		log.Printf("Failed to extract SUPL INIT from WAP Push SMS: %v", err)
		return
	}

	var pdu oma_ulp.ULPPDU
	if err := oma_ulp.Unmarshal(rawSuplInit, &pdu); err != nil {
		log.Printf("Failed to unmarshal SUPL INIT PDU: %v", err)
		return
	}

	slpSessID := pdu.SessionID.SlpSessionID.SessionID
	log.Printf("Received SUPL INIT via SMS: SLP Session ID = %x", slpSessID)

	conn, err := net.Dial("tcp", gwAddr)
	if err != nil {
		log.Printf("Failed to dial ULP Gateway %s: %v", gwAddr, err)
		return
	}
	defer conn.Close()

	if err := sendSuplPosInit(conn, slpSessID, authKey, rawSuplInit); err != nil {
		log.Printf("Failed to send SUPL POS INIT: %v", err)
		return
	}
	log.Printf("Sent SUPL POS INIT to ULP Gateway with VER")
}

func dummyBitString() {
	_ = uper.BitString{}
}
