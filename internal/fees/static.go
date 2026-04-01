package fees

import (
	"context"
	"math"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/primexz/recharge-lnd/internal/config"
	lndclient "github.com/primexz/recharge-lnd/internal/lnd"
	"go.uber.org/zap"
)

type StaticPolicies struct {
	policies []config.PolicyConfig
	client   *lndclient.Client
	logger   *zap.Logger
	dryRun   bool
}

func NewStaticPolicies(policies []config.PolicyConfig, client *lndclient.Client, logger *zap.Logger, dryRun bool) *StaticPolicies {
	return &StaticPolicies{policies: policies, client: client, logger: logger, dryRun: dryRun}
}

type MatchResult struct {
	PolicyName     string
	ChanID         uint64
	FeePPM         int64
	BaseFee        int64
	InboundFeePPM  int32
	InboundBaseFee int32
	TimeLockDelta  uint32
}

func (s *StaticPolicies) Run(ctx context.Context, channels []*lnrpc.Channel) (map[uint64]bool, error) {
	matched := make(map[uint64]bool)

	nodeInfo, err := s.client.GetInfo(ctx)
	syncedToChain := true
	if err != nil {
		s.logger.Warn("failed to get node info, assuming synced", zap.Error(err))
	} else {
		syncedToChain = nodeInfo.SyncedToChain
	}

	for _, ch := range channels {
		edge, err := s.client.GetChanInfo(ctx, ch.ChanId)
		if err != nil {
			s.logger.Error("failed to get channel info",
				zap.Uint64("chan_id", ch.ChanId),
				zap.Error(err),
			)
			continue
		}

		result, ok := s.matchChannel(ch, edge, syncedToChain)
		if !ok {
			continue
		}
		matched[ch.ChanId] = true

		alias := s.client.GetNodeAlias(ctx, ch.RemotePubkey)
		ratio := float64(ch.LocalBalance) / float64(ch.Capacity)

		currentFee := currentOutboundFeePPM(edge, ch.RemotePubkey)
		currentInbound := currentInboundFee(edge, ch.RemotePubkey)

		if currentFee == result.FeePPM && currentInbound == result.InboundFeePPM {
			s.logger.Debug("policy already applied",
				zap.String("peer", alias),
				zap.String("policy", result.PolicyName),
				zap.Int64("fee_ppm", result.FeePPM),
			)
			continue
		}

		if s.dryRun {
			s.logger.Info("[dry-run] would apply static policy",
				zap.String("peer", alias),
				zap.Uint64("chan_id", ch.ChanId),
				zap.String("policy", result.PolicyName),
				zap.Float64("ratio", math.Round(ratio*1000)/1000),
				zap.Int64("old_fee_ppm", currentFee),
				zap.Int64("new_fee_ppm", result.FeePPM),
				zap.Int32("old_inbound_fee_ppm", currentInbound),
				zap.Int32("new_inbound_fee_ppm", result.InboundFeePPM),
			)
			continue
		}

		s.logger.Info("applying static policy",
			zap.String("peer", alias),
			zap.Uint64("chan_id", ch.ChanId),
			zap.String("policy", result.PolicyName),
			zap.Float64("ratio", math.Round(ratio*1000)/1000),
			zap.Int64("old_fee_ppm", currentFee),
			zap.Int64("new_fee_ppm", result.FeePPM),
			zap.Int32("inbound_fee_ppm", result.InboundFeePPM),
		)

		if err := s.client.UpdateChannelPolicy(ctx, ch.ChannelPoint,
			safeUint32(result.FeePPM), result.BaseFee,
			result.InboundFeePPM, result.InboundBaseFee,
			result.TimeLockDelta,
		); err != nil {
			s.logger.Error("failed to apply policy",
				zap.String("peer", alias),
				zap.Uint64("chan_id", ch.ChanId),
				zap.Error(err),
			)
		}
	}

	return matched, nil
}

func (s *StaticPolicies) matchChannel(ch *lnrpc.Channel, edge *lnrpc.ChannelEdge, syncedToChain bool) (MatchResult, bool) {
	if ch.Capacity == 0 {
		return MatchResult{}, false
	}

	ratio := float64(ch.LocalBalance) / float64(ch.Capacity)

	for _, p := range s.policies {
		if len(p.Channels) > 0 {
			if !isExcluded(ch.ChanId, p.Channels) {
				continue
			}
		}

		if p.MinRatio != nil && ratio < *p.MinRatio {
			continue
		}
		if p.MaxRatio != nil && ratio > *p.MaxRatio {
			continue
		}

		if p.Private != nil && ch.Private != *p.Private {
			continue
		}

		if p.SyncedToChain != nil && syncedToChain != *p.SyncedToChain {
			continue
		}

		peerFeePPM := int64(0)
		if edge != nil {
			peerFeePPM = currentOutboundFeePPM(edge, ch.RemotePubkey)
		}
		if p.MinPeerFeePPM != nil && peerFeePPM < *p.MinPeerFeePPM {
			continue
		}

		result := MatchResult{
			PolicyName:     p.Name,
			ChanID:         ch.ChanId,
			BaseFee:        p.BaseFee,
			InboundFeePPM:  p.InboundFeePPM,
			InboundBaseFee: p.InboundBaseFee,
			TimeLockDelta:  p.TimeLockDelta,
		}

		if result.TimeLockDelta == 0 {
			result.TimeLockDelta = 40
		}

		switch p.Strategy {
		case "static":
			result.FeePPM = p.FeePPM
		case "proportional":
			minR, maxR := 0.0, 0.0
			if p.MinRatio != nil {
				minR = *p.MinRatio
			}
			if p.MaxRatio != nil {
				maxR = *p.MaxRatio
			}
			result.FeePPM = proportionalFee(ratio, minR, maxR, p.MinFeePPM, p.MaxFeePPM)
		case "match_peer":
			result.FeePPM = peerFeePPM
		}

		return result, true
	}

	return MatchResult{}, false
}

func proportionalFee(ratio, minRatio, maxRatio float64, minFee, maxFee int64) int64 {
	if maxRatio <= minRatio {
		return minFee
	}

	normalized := (ratio - minRatio) / (maxRatio - minRatio)
	if normalized < 0 {
		normalized = 0
	}
	if normalized > 1 {
		normalized = 1
	}

	fee := float64(maxFee) - normalized*float64(maxFee-minFee)
	return int64(math.Round(fee))
}

func currentInboundFee(edge *lnrpc.ChannelEdge, remotePubkey string) int32 {
	if edge.Node1Pub == remotePubkey {
		if edge.Node2Policy != nil {
			return edge.Node2Policy.InboundFeeRateMilliMsat
		}
	} else {
		if edge.Node1Policy != nil {
			return edge.Node1Policy.InboundFeeRateMilliMsat
		}
	}
	return 0
}
