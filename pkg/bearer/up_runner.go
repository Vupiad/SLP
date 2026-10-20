package bearer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"slp/pkg/metrics"
	"slp/pkg/oma_ulp"
	"slp/pkg/posinit"
	"slp/pkg/session"
	"slp/pkg/smspush"
)

// SmsSender defines the interface for delivering WAP Push SMS to the terminal
type SmsSender interface {
	SubmitWapPushSMS(ctx context.Context, destMsisdn string, wapPushData []byte) (string, error)
}

// UPRunner executes the Network-Initiated SUPL 2.0 User Plane workflow
type UPRunner struct {
	sessionStore session.SessionStore
	smsSender    SmsSender
	processor    *posinit.Processor
	defaultKey   []byte
	slpFqdn      string
	counters     *metrics.TelemetryCounters
}

// NewUPRunner initializes a new UPRunner instance
func NewUPRunner(
	store session.SessionStore,
	sms SmsSender,
	proc *posinit.Processor,
	defaultKey []byte,
	slpFqdn string,
	counters *metrics.TelemetryCounters,
) *UPRunner {
	if len(defaultKey) == 0 {
		defaultKey = []byte("slp-5g-default-integrity-root-key")
	}
	if slpFqdn == "" {
		slpFqdn = "slp.viettel.vn"
	}
	if counters == nil {
		counters = metrics.DefaultCounters
	}
	return &UPRunner{
		sessionStore: store,
		smsSender:    sms,
		processor:    proc,
		defaultKey:   defaultKey,
		slpFqdn:      slpFqdn,
		counters:     counters,
	}
}

// ExecuteUP runs the complete Figure 1 User Plane location workflow
func (r *UPRunner) ExecuteUP(ctx context.Context, req LocationRequest) (*session.LocationResult, error) {
	r.counters.UpRequests.Add(1)

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	// 1. Create session with 4-byte random ID
	sess, err := session.NewSession(req.TargetID, timeout+20*time.Second, req.QoP)
	if err != nil {
		r.counters.UpFailed.Add(1)
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	authKey := req.AuthKey
	if len(authKey) == 0 {
		authKey = r.defaultKey
	}
	sess.AuthKey = authKey

	// 2. Construct SUPL INIT PDU
	suplInitPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sess.SessionID[:],
				SlpId: oma_ulp.SLPAddress{
					Choice: oma_ulp.SLPAddressChoice_Fqdn,
					Fqdn:   ptrFQDN(r.slpFqdn),
				},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLINIT,
			MsSUPLINIT: &oma_ulp.SUPLINIT{
				PosMethod: oma_ulp.PosMethod_AgpsSETassisted,
				SLPMode:   oma_ulp.SLPMode_Proxy,
			},
		},
	}

	// 3. Compute VER (HMAC-SHA256-64) over SUPL INIT
	ver, suplInitBytes, err := oma_ulp.ComputeVerForPdu(authKey, &suplInitPdu)
	if err != nil {
		r.counters.UpFailed.Add(1)
		return nil, fmt.Errorf("failed to encode SUPL INIT and compute VER: %w", err)
	}
	sess.VER = ver.Bytes[:8]
	sess.SuplInitPdu = suplInitBytes

	if err := r.sessionStore.SaveSession(ctx, sess); err != nil {
		r.counters.UpFailed.Add(1)
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	// 4. Package SUPL INIT into WAP Push PDU (UDH port 2948 + WSP Push)
	wapPushData := smspush.BuildWapPushSmsPackage(0x01, suplInitBytes)

	// 5. Register waiter for terminal response
	waiterCh := r.processor.RegisterWaiter(sess.SessionID)
	defer r.processor.UnregisterWaiter(sess.SessionID)

	// 6. Send WAP Push SMS to terminal via SMSC
	if r.smsSender != nil {
		smsCtx, cancelSms := context.WithTimeout(ctx, 3*time.Second)
		_, err = r.smsSender.SubmitWapPushSMS(smsCtx, req.TargetID, wapPushData)
		cancelSms()
		if err != nil {
			r.counters.SmsSentFailure.Add(1)
			r.counters.UpFailed.Add(1)
			sess.FailReason = fmt.Sprintf("SMS wake-up failed: %v", err)
			_ = sess.SetState(session.StateFailed)
			_ = r.sessionStore.UpdateSession(ctx, sess)
			return nil, fmt.Errorf("smpp sms delivery failed: %w", err)
		}
		r.counters.SmsSentSuccess.Add(1)
	}

	_ = sess.SetState(session.StateWakeupSent)
	_ = r.sessionStore.UpdateSession(ctx, sess)

	// 7. Await SUPL POS INIT and completion
	select {
	case <-ctx.Done():
		r.counters.UpFailed.Add(1)
		sess.FailReason = "context cancelled"
		_ = sess.SetState(session.StateFailed)
		_ = r.sessionStore.UpdateSession(ctx, sess)
		return nil, ctx.Err()

	case <-time.After(timeout):
		r.counters.UpFailed.Add(1)
		sess.FailReason = "terminal response timeout"
		_ = sess.SetState(session.StateFailed)
		_ = r.sessionStore.UpdateSession(ctx, sess)
		return nil, errors.New("timeout waiting for SUPL POS INIT from terminal")

	case result := <-waiterCh:
		if result == nil {
			r.counters.UpFailed.Add(1)
			return nil, errors.New("nil location result received")
		}
		r.counters.UpSuccess.Add(1)
		r.counters.VerMatch.Add(1)
		return result, nil
	}
}

func ptrFQDN(s string) *oma_ulp.FQDN {
	v := oma_ulp.FQDN(s)
	return &v
}
