package lnd

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Client struct {
	Host         string
	Port         int
	TLSCertPath  string
	MacaroonPath string

	conn   *grpc.ClientConn
	ln     lnrpc.LightningClient
	router routerrpc.RouterClient
	mu     sync.Mutex
	logger *zap.Logger
}

func NewClient(host string, port int, tlsCertPath, macaroonPath string, logger *zap.Logger) *Client {
	return &Client{
		Host:         host,
		Port:         port,
		TLSCertPath:  tlsCertPath,
		MacaroonPath: macaroonPath,
		logger:       logger,
	}
}

func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	tlsCert, err := credentials.NewClientTLSFromFile(c.TLSCertPath, "")
	if err != nil {
		return fmt.Errorf("reading TLS cert: %w", err)
	}

	macBytes, err := os.ReadFile(c.MacaroonPath)
	if err != nil {
		return fmt.Errorf("reading macaroon: %w", err)
	}

	conn, err := grpc.NewClient(
		fmt.Sprintf("%s:%d", c.Host, c.Port),
		grpc.WithTransportCredentials(tlsCert),
		grpc.WithPerRPCCredentials(&MacaroonCredential{
			MacaroonHex: hex.EncodeToString(macBytes),
		}),
	)
	if err != nil {
		return fmt.Errorf("connecting to LND: %w", err)
	}

	c.conn = conn
	c.ln = lnrpc.NewLightningClient(conn)
	c.router = routerrpc.NewRouterClient(conn)

	c.logger.Info("connected to LND", zap.String("host", c.Host), zap.Int("port", c.Port))
	return nil
}

func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			return fmt.Errorf("closing connection: %w", err)
		}
		c.conn = nil
		c.ln = nil
		c.router = nil
	}
	return nil
}

func (c *Client) GetInfo(ctx context.Context) (*lnrpc.GetInfoResponse, error) {
	return c.ln.GetInfo(ctx, &lnrpc.GetInfoRequest{})
}

func (c *Client) ListChannels(ctx context.Context) ([]*lnrpc.Channel, error) {
	resp, err := c.ln.ListChannels(ctx, &lnrpc.ListChannelsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Channels, nil
}

func (c *Client) GetChanInfo(ctx context.Context, chanID uint64) (*lnrpc.ChannelEdge, error) {
	return c.ln.GetChanInfo(ctx, &lnrpc.ChanInfoRequest{ChanId: chanID})
}

func (c *Client) GetNodeAlias(ctx context.Context, pubkey string) string {
	info, err := c.ln.GetNodeInfo(ctx, &lnrpc.NodeInfoRequest{PubKey: pubkey})
	if err != nil || info.Node == nil {
		return pubkey[:12]
	}
	if info.Node.Alias == "" {
		return pubkey[:12]
	}
	return info.Node.Alias
}

func (c *Client) ForwardingHistory(ctx context.Context, startTime, endTime time.Time) ([]*lnrpc.ForwardingEvent, error) {
	var allEvents []*lnrpc.ForwardingEvent
	var indexOffset uint32

	for {
		resp, err := c.ln.ForwardingHistory(ctx, &lnrpc.ForwardingHistoryRequest{
			StartTime:    uint64(startTime.Unix()),
			EndTime:      uint64(endTime.Unix()),
			IndexOffset:  indexOffset,
			NumMaxEvents: 10000,
		})
		if err != nil {
			return nil, fmt.Errorf("fetching forwarding history: %w", err)
		}

		allEvents = append(allEvents, resp.ForwardingEvents...)

		if resp.LastOffsetIndex == indexOffset || len(resp.ForwardingEvents) == 0 {
			break
		}
		indexOffset = resp.LastOffsetIndex
	}

	return allEvents, nil
}

func (c *Client) UpdateChannelPolicy(ctx context.Context, chanPoint string, feePPM uint32, baseFee int64, inboundFeePPM int32, inboundBaseFee int32, timeLockDelta uint32) error {
	txid, outputIdx, err := parseChanPoint(chanPoint)
	if err != nil {
		return fmt.Errorf("parsing channel point: %w", err)
	}

	req := &lnrpc.PolicyUpdateRequest{
		Scope: &lnrpc.PolicyUpdateRequest_ChanPoint{
			ChanPoint: &lnrpc.ChannelPoint{
				FundingTxid: &lnrpc.ChannelPoint_FundingTxidStr{
					FundingTxidStr: txid,
				},
				OutputIndex: outputIdx,
			},
		},
		BaseFeeMsat:   baseFee,
		FeeRatePpm:    feePPM,
		TimeLockDelta: timeLockDelta,
		InboundFee: &lnrpc.InboundFee{
			FeeRatePpm:  inboundFeePPM,
			BaseFeeMsat: inboundBaseFee,
		},
	}

	_, err = c.ln.UpdateChannelPolicy(ctx, req)
	return err
}

func parseChanPoint(cp string) (string, uint32, error) {
	var txid string
	var idx uint32
	_, err := fmt.Sscanf(cp, "%64s:%d", &txid, &idx)
	if err != nil {
		for i := len(cp) - 1; i >= 0; i-- {
			if cp[i] == ':' {
				txid = cp[:i]
				_, err = fmt.Sscanf(cp[i+1:], "%d", &idx)
				if err != nil {
					return "", 0, fmt.Errorf("invalid channel point: %s", cp)
				}
				return txid, idx, nil
			}
		}
		return "", 0, fmt.Errorf("invalid channel point: %s", cp)
	}
	return txid, idx, nil
}
