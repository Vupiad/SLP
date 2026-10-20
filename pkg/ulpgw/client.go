package ulpgw

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// BackoffConfig defines exponential backoff parameters for stream reconnection
type BackoffConfig struct {
	InitialInterval     time.Duration
	Multiplier          float64
	MaxInterval         time.Duration
	RandomizationFactor float64
}

// DefaultBackoffConfig returns sensible defaults for exponential backoff
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialInterval:     200 * time.Millisecond,
		Multiplier:          2.0,
		MaxInterval:         5 * time.Second,
		RandomizationFactor: 0.1,
	}
}

// KeepaliveConfig defines keepalive heartbeat intervals
type KeepaliveConfig struct {
	Interval time.Duration // Interval between heartbeats
	Timeout  time.Duration // Time without pong before reconnecting
}

// DefaultKeepaliveConfig returns sensible defaults for keepalive
func DefaultKeepaliveConfig() KeepaliveConfig {
	return KeepaliveConfig{
		Interval: 2 * time.Second,
		Timeout:  4 * time.Second,
	}
}

// ClientConfig holds all settings for the ULP Gateway Client
type ClientConfig struct {
	GatewayAddr string
	Backoff     BackoffConfig
	Keepalive   KeepaliveConfig
	SendBufSize int
}

// UlpGatewayClient maintains a resilient, long-lived bidirectional gRPC stream to ULP Gateway
type UlpGatewayClient struct {
	config       ClientConfig
	handler      func(frame *UlpFrame)
	sendCh       chan *UlpFrame
	stopCh       chan struct{}
	doneCh       chan struct{}
	connected    atomic.Bool
	lastPongTime atomic.Int64 // Unix nano
	mu           sync.RWMutex
}

// NewUlpGatewayClient initializes a new ULP Gateway client
func NewUlpGatewayClient(cfg ClientConfig) *UlpGatewayClient {
	if cfg.Backoff.InitialInterval == 0 {
		cfg.Backoff = DefaultBackoffConfig()
	}
	if cfg.Keepalive.Interval == 0 {
		cfg.Keepalive = DefaultKeepaliveConfig()
	}
	if cfg.SendBufSize <= 0 {
		cfg.SendBufSize = 128
	}

	client := &UlpGatewayClient{
		config: cfg,
		sendCh: make(chan *UlpFrame, cfg.SendBufSize),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	return client
}

// SetMessageHandler sets the callback for incoming frames from the gateway
func (c *UlpGatewayClient) SetMessageHandler(handler func(frame *UlpFrame)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handler = handler
}

// IsConnected returns whether the gRPC stream is currently established and passing health checks
func (c *UlpGatewayClient) IsConnected() bool {
	return c.connected.Load()
}

// Start initiates the long-running stream manager in the background
func (c *UlpGatewayClient) Start(ctx context.Context) {
	go c.runLoop(ctx)
}

func (c *UlpGatewayClient) runLoop(ctx context.Context) {
	defer close(c.doneCh)

	curInterval := c.config.Backoff.InitialInterval

	for {
		select {
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		err := c.connectAndStream(ctx)
		c.connected.Store(false)

		select {
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		// Calculate backoff with jitter
		jitter := (rand.Float64()*2 - 1) * c.config.Backoff.RandomizationFactor
		sleepDuration := time.Duration(float64(curInterval) * (1.0 + jitter))
		if sleepDuration < 0 {
			sleepDuration = curInterval
		}

		time.Sleep(sleepDuration)

		// Exponential increase capped at MaxInterval
		curInterval = time.Duration(float64(curInterval) * c.config.Backoff.Multiplier)
		if curInterval > c.config.Backoff.MaxInterval {
			curInterval = c.config.Backoff.MaxInterval
		}

		_ = err
	}
}

func (c *UlpGatewayClient) connectAndStream(ctx context.Context) error {
	conn, err := grpc.NewClient(
		c.config.GatewayAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(UlpCodec{})),
	)
	if err != nil {
		return fmt.Errorf("grpc dial error: %w", err)
	}
	defer conn.Close()

	streamDesc := &grpc.StreamDesc{
		StreamName:    "StreamUlp",
		ServerStreams: true,
		ClientStreams: true,
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	clientStream, err := conn.NewStream(streamCtx, streamDesc, StreamUlpMethodName)
	if err != nil {
		return fmt.Errorf("new stream error: %w", err)
	}

	// Reset backoff on successful stream establishment
	c.connected.Store(true)
	c.lastPongTime.Store(time.Now().UnixNano())

	errCh := make(chan error, 3)

	// Sender goroutine
	go func() {
		for {
			select {
			case <-c.stopCh:
				return
			case <-streamCtx.Done():
				return
			case frame := <-c.sendCh:
				if err := clientStream.SendMsg(frame); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// Receiver goroutine
	go func() {
		for {
			var frame UlpFrame
			if err := clientStream.RecvMsg(&frame); err != nil {
				errCh <- err
				return
			}

			if frame.IsHeartbeat {
				// Record successful heartbeat pong
				c.lastPongTime.Store(time.Now().UnixNano())
				continue
			}

			// Deliver data frame to handler
			c.mu.RLock()
			handler := c.handler
			c.mu.RUnlock()

			if handler != nil {
				handler(&frame)
			}
		}
	}()

	// Keepalive heartbeat goroutine
	go func() {
		ticker := time.NewTicker(c.config.Keepalive.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-c.stopCh:
				return
			case <-streamCtx.Done():
				return
			case <-ticker.C:
				// 1. Check if last pong timed out
				lastPong := time.Unix(0, c.lastPongTime.Load())
				if time.Since(lastPong) > c.config.Keepalive.Timeout {
					errCh <- errors.New("heartbeat keepalive timeout detected")
					return
				}

				// 2. Send keepalive ping
				ping := NewHeartbeatFrame()
				select {
				case c.sendCh <- ping:
				default:
					// Send buffer full, connection might be stalled
					errCh <- errors.New("send buffer full on heartbeat ping")
					return
				}
			}
		}
	}()

	// Wait for stream error or shutdown
	select {
	case <-c.stopCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// SendPdu queues a UPER-encoded PDU to be sent over the stream to ULP Gateway
func (c *UlpGatewayClient) SendPdu(sessionID []byte, ueID string, payload []byte) error {
	frame := NewDataFrame(sessionID, ueID, payload)
	select {
	case c.sendCh <- frame:
		return nil
	default:
		return errors.New("gateway client send queue full")
	}
}

// Close stops the client and closes the stream
func (c *UlpGatewayClient) Close() error {
	select {
	case <-c.stopCh:
		return nil
	default:
		close(c.stopCh)
		<-c.doneCh
		return nil
	}
}
