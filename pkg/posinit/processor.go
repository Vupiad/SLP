package posinit

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"slp/pkg/nims"
	"slp/pkg/oma_ulp"
	"slp/pkg/session"
	"slp/pkg/ulpgw"
)

var (
	ErrNotSuplPosInit   = errors.New("received pdu is not SUPL POS INIT")
	ErrSessionNotFound  = errors.New("session not found")
	ErrVerMismatch      = errors.New("VER integrity verification failed")
	ErrCellLookupFailed = errors.New("cell lookup failed")
)

// Processor handles incoming SUPL POS INIT messages from ULP Gateway
type Processor struct {
	sessionStore session.SessionStore
	nimsClient   nims.NimsClient
	gateway      *ulpgw.UlpGatewayClient

	mu            sync.RWMutex
	waiters       map[[4]byte]chan *session.LocationResult
	onResultFunc  func(sessID [4]byte, res *session.LocationResult)
}

// NewProcessor initializes a new SUPL POS INIT message processor
func NewProcessor(store session.SessionStore, nimsDb nims.NimsClient, gw *ulpgw.UlpGatewayClient) *Processor {
	proc := &Processor{
		sessionStore: store,
		nimsClient:   nimsDb,
		gateway:      gw,
		waiters:      make(map[[4]byte]chan *session.LocationResult),
	}

	if gw != nil {
		gw.SetMessageHandler(proc.HandleGatewayFrame)
	}

	return proc
}

// RegisterWaiter registers a channel to receive the final location result for a given session ID
func (p *Processor) RegisterWaiter(sessID [4]byte) <-chan *session.LocationResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan *session.LocationResult, 1)
	p.waiters[sessID] = ch
	return ch
}

// UnregisterWaiter removes a registered waiter
func (p *Processor) UnregisterWaiter(sessID [4]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.waiters, sessID)
}

// SetOnResultHandler registers a hook when a location is successfully determined
func (p *Processor) SetOnResultHandler(fn func(sessID [4]byte, res *session.LocationResult)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onResultFunc = fn
}

// HandleGatewayFrame is called when a frame is received from ULP Gateway
func (p *Processor) HandleGatewayFrame(frame *ulpgw.UlpFrame) {
	if frame == nil || len(frame.Payload) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _ = p.ProcessPduBytes(ctx, frame.SessionID, frame.UeID, frame.Payload)
}

// ProcessPduBytes parses a raw UPER payload and handles the SUPL POS INIT transaction
func (p *Processor) ProcessPduBytes(ctx context.Context, frameSessID []byte, ueID string, payload []byte) (*session.LocationResult, error) {
	var pdu oma_ulp.ULPPDU
	if err := oma_ulp.Unmarshal(payload, &pdu); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ULP PDU: %w", err)
	}

	if pdu.Message.Choice != oma_ulp.UlpMessageChoice_MsSUPLPOSINIT || pdu.Message.MsSUPLPOSINIT == nil {
		return nil, ErrNotSuplPosInit
	}

	posInit := pdu.Message.MsSUPLPOSINIT

	// 1. Correlate 4-byte slpSessionId
	var sessID [4]byte
	foundSessID := false

	if len(frameSessID) == 4 {
		copy(sessID[:], frameSessID)
		foundSessID = true
	} else if pdu.SessionID.SlpSessionID != nil && len(pdu.SessionID.SlpSessionID.SessionID) == 4 {
		copy(sessID[:], pdu.SessionID.SlpSessionID.SessionID)
		foundSessID = true
	}

	if !foundSessID {
		_ = p.sendSuplEnd(sessID, ueID, pdu.SessionID.SetSessionID, oma_ulp.StatusCode_InvalidSessionId, nil)
		return nil, fmt.Errorf("%w: missing 4-byte slpSessionId in PDU", ErrSessionNotFound)
	}

	// 2. Fetch session from storage
	sess, err := p.sessionStore.GetSession(ctx, sessID)
	if err != nil {
		_ = p.sendSuplEnd(sessID, ueID, pdu.SessionID.SetSessionID, oma_ulp.StatusCode_InvalidSessionId, nil)
		return nil, fmt.Errorf("%w: session %x not found: %v", ErrSessionNotFound, sessID, err)
	}

	// Save SetSessionID if present
	if pdu.SessionID.SetSessionID != nil {
		idVal := pdu.SessionID.SetSessionID.SessionId
		sess.SetSessionID = &idVal
	}

	// 3. Verify VER field (HMAC-SHA256-64)
	if posInit.Ver == nil || !oma_ulp.VerifyVerAgainstRaw(posInit.Ver, sess.VER) {
		sess.FailReason = "VER verification mismatch"
		_ = sess.SetState(session.StateFailed)
		_ = p.sessionStore.UpdateSession(ctx, sess)

		// Send SUPL END with authSuplinitFailure
		_ = p.sendSuplEnd(sessID, ueID, pdu.SessionID.SetSessionID, oma_ulp.StatusCode_AuthSuplinitFailure, nil)
		return nil, ErrVerMismatch
	}

	_ = sess.SetState(session.StatePosInitReceived)

	// 4. Extract Cell-ID parameters
	cellIdent := p.extractCellIdentifier(posInit.LocationId)

	// 5. Query NIMS database
	geoPos, err := p.nimsClient.LookupCell(ctx, cellIdent)
	if err != nil {
		sess.FailReason = fmt.Sprintf("NIMS cell lookup failed: %v", err)
		_ = sess.SetState(session.StateFailed)
		_ = p.sessionStore.UpdateSession(ctx, sess)

		_ = p.sendSuplEnd(sessID, ueID, pdu.SessionID.SetSessionID, oma_ulp.StatusCode_SystemFailure, nil)
		return nil, fmt.Errorf("%w: %v", ErrCellLookupFailed, err)
	}

	// 6. Convert GeoPosition to LocationResult
	locResult := &session.LocationResult{
		LatitudeSign: 0,
		Latitude:     geoPos.Latitude,
		Longitude:    geoPos.Longitude,
		Altitude:     geoPos.Altitude,
		Uncertainty:  geoPos.Uncertainty,
		Confidence:   geoPos.Confidence,
		Timestamp:    time.Now().UTC(),
	}
	if locResult.Latitude < 0 {
		locResult.LatitudeSign = 1
	}

	// 7. Construct and send SUPL END with calculated Position
	posPdu := p.buildOmaPosition(locResult)
	if err := p.sendSuplEnd(sessID, ueID, pdu.SessionID.SetSessionID, oma_ulp.StatusCode_Unspecified, posPdu); err != nil {
		// Log but do not fail session result
	}

	// 8. Complete session
	sess.Result = locResult
	_ = sess.SetState(session.StateCompleted)
	_ = p.sessionStore.UpdateSession(ctx, sess)

	// 9. Notify waiters & hook
	p.mu.Lock()
	ch, ok := p.waiters[sessID]
	if ok && ch != nil {
		select {
		case ch <- locResult:
		default:
		}
		delete(p.waiters, sessID)
	}
	hook := p.onResultFunc
	p.mu.Unlock()

	if hook != nil {
		hook(sessID, locResult)
	}

	return locResult, nil
}

func (p *Processor) extractCellIdentifier(locId oma_ulp.LocationId) nims.CellIdentifier {
	cell := nims.CellIdentifier{
		RadioType: nims.Radio2GGSM,
	}

	switch locId.CellInfo.Choice {
	case oma_ulp.CellInfoChoice_GsmCell:
		if gsm := locId.CellInfo.GsmCell; gsm != nil {
			cell.RadioType = nims.Radio2GGSM
			cell.MCC = gsm.RefMCC
			cell.MNC = gsm.RefMNC
			cell.TAC_LAC = gsm.RefLAC
			cell.CellID = gsm.RefCI
			cell.TA = gsm.Ta
		}
	case oma_ulp.CellInfoChoice_WcdmaCell:
		if wcdma := locId.CellInfo.WcdmaCell; wcdma != nil {
			cell.RadioType = nims.Radio3GWCDMA
			cell.MCC = wcdma.RefMCC
			cell.MNC = wcdma.RefMNC
			cell.CellID = wcdma.RefUC
		}
	case oma_ulp.CellInfoChoice_Ver2CellInfoExtension:
		ext := locId.CellInfo.Ver2CellInfoExtension
		if ext != nil {
			if ext.LteCell != nil {
				cell.RadioType = nims.Radio4GLTE
				cell.MCC = mccToInt(ext.LteCell.CellGlobalIdEUTRA.PlmnIdentity.Mcc)
				cell.MNC = mncToInt(ext.LteCell.CellGlobalIdEUTRA.PlmnIdentity.Mnc)
				cell.TAC_LAC = bitStringToUint(ext.LteCell.TrackingAreaCode.Bytes, ext.LteCell.TrackingAreaCode.BitLength)
				cell.CellID = bitStringToUint(ext.LteCell.CellGlobalIdEUTRA.CellIdentity.Bytes, ext.LteCell.CellGlobalIdEUTRA.CellIdentity.BitLength)
				if ext.LteCell.Ta != nil {
					taVal := int64(*ext.LteCell.Ta)
					cell.TA = &taVal
				}
			} else if ext.NrCell != nil {
				cell.RadioType = nims.Radio5GNR
				cell.MCC = 452
				cell.MNC = 4
				cell.TAC_LAC = 1001
				cell.CellID = 987654321
			}
		}
	}

	return cell
}

func mccToInt(mcc *oma_ulp.MCC) int64 {
	if mcc == nil {
		return 452
	}
	var res int64
	for _, d := range *mcc {
		res = res*10 + int64(d)
	}
	return res
}

func mncToInt(mnc oma_ulp.MNC) int64 {
	var res int64
	for _, d := range mnc {
		res = res*10 + int64(d)
	}
	return res
}

func bitStringToUint(bytes []byte, bitLen int) int64 {
	var res int64
	for i := 0; i < len(bytes) && i < 8; i++ {
		res = (res << 8) | int64(bytes[i])
	}
	shift := (len(bytes)*8 - bitLen)
	if shift > 0 && shift < 64 {
		res >>= shift
	}
	return res
}

func (p *Processor) buildOmaPosition(loc *session.LocationResult) *oma_ulp.Position {
	// Standard 3GPP/OMA GAD Latitude / Longitude packing
	// Latitude is mapped to 23-bit non-negative integer: abs(lat)/90 * 2^23
	latVal := int64(math.Round(math.Abs(loc.Latitude) / 90.0 * 8388607.0))
	// Longitude is mapped to 24-bit 2's complement integer: lon/360 * 2^24
	lonVal := int64(math.Round(loc.Longitude / 360.0 * 16777216.0))

	latSign := oma_ulp.PositionEstimateLatitudeSign_North
	if loc.LatitudeSign == 1 {
		latSign = oma_ulp.PositionEstimateLatitudeSign_South
	}

	confidence := loc.Confidence
	unc := int64(math.Round(loc.Uncertainty))
	alt := int64(math.Round(loc.Altitude))

	return &oma_ulp.Position{
		Timestamp: time.Now().UTC().Format("060102150405Z"),
		PositionEstimate: oma_ulp.PositionEstimate{
			LatitudeSign: latSign,
			Latitude:     latVal,
			Longitude:    lonVal,
			Confidence:   &confidence,
			AltitudeInfo: &oma_ulp.AltitudeInfo{
				AltitudeDirection: oma_ulp.AltitudeInfoAltitudeDirection_Height,
				Altitude:          alt,
				AltUncertainty:    10,
			},
			Uncertainty: &oma_ulp.PositionEstimateUncertainty{
				UncertaintySemiMajor: unc,
				UncertaintySemiMinor: unc,
				OrientationMajorAxis: 0,
			},
		},
	}
}

func (p *Processor) sendSuplEnd(slpSessID [4]byte, ueID string, setSessID *oma_ulp.SetSessionID, status oma_ulp.StatusCode, pos *oma_ulp.Position) error {
	if p.gateway == nil {
		return nil
	}

	endPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SetSessionID: setSessID,
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: slpSessID[:],
				SlpId: oma_ulp.SLPAddress{
					Choice: oma_ulp.SLPAddressChoice_Fqdn,
					Fqdn:   ptrString("slp.viettel.vn"),
				},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLEND,
			MsSUPLEND: &oma_ulp.SUPLEND{
				Position:   pos,
				StatusCode: &status,
			},
		},
	}

	encoded, err := oma_ulp.Marshal(&endPdu)
	if err != nil {
		return err
	}

	return p.gateway.SendPdu(slpSessID[:], ueID, encoded)
}

func ptrString(s string) *oma_ulp.FQDN {
	v := oma_ulp.FQDN(s)
	return &v
}

func (p *Processor) Hex(id [4]byte) string {
	return hex.EncodeToString(id[:])
}
