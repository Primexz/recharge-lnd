package fees

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/primexz/recharge-lnd/internal/config"
	lndclient "github.com/primexz/recharge-lnd/internal/lnd"
	"go.uber.org/zap"
)

type AutoFees struct {
	cfg    config.AutoFeesConfig
	client *lndclient.Client
	logger *zap.Logger
	dryRun bool
}

func NewAutoFees(cfg config.AutoFeesConfig, client *lndclient.Client, logger *zap.Logger, dryRun bool) *AutoFees {
	return &AutoFees{cfg: cfg, client: client, logger: logger, dryRun: dryRun}
}

type channelThroughput struct {
	ChanID     uint64
	AmtOutSat  int64
	EventCount int64
}

func (a *AutoFees) Run(ctx context.Context, channels []*lnrpc.Channel, excluded map[uint64]bool) error {
	now := time.Now()
	refStart := now.Add(-a.cfg.ReferencePeriod)
	analysisStart := now.Add(-a.cfg.AnalysisPeriod)

	// Build set of autofee-managed channel IDs.
	managedChannels := make(map[uint64]bool)
	for _, ch := range channels {
		if excluded[ch.ChanId] || isExcluded(ch.ChanId, a.cfg.ExcludeChannels) {
			continue
		}
		managedChannels[ch.ChanId] = true
	}

	// Fetch full reference period for target calculation.
	refEvents, err := a.client.ForwardingHistory(ctx, refStart, now)
	if err != nil {
		return err
	}
	a.logger.Info("fetched reference forwarding history",
		zap.Int("events", len(refEvents)),
		zap.Duration("period", a.cfg.ReferencePeriod),
	)

	refMap := buildThroughputMap(refEvents, managedChannels)

	referenceDays := a.cfg.ReferencePeriod.Hours() / 24
	targetRate := a.calculateTargetRate(refMap, referenceDays)
	a.logger.Info("calculated target rate",
		zap.Float64("target_sat_per_day", targetRate),
		zap.Int("top_peers", a.cfg.TopPeersCount),
	)

	// Fetch shorter analysis period for recent per-channel performance.
	analysisEvents, err := a.client.ForwardingHistory(ctx, analysisStart, now)
	if err != nil {
		return err
	}
	a.logger.Info("fetched analysis forwarding history",
		zap.Int("events", len(analysisEvents)),
		zap.Duration("period", a.cfg.AnalysisPeriod),
	)

	analysisMap := buildThroughputMap(analysisEvents, managedChannels)
	analysisDays := a.cfg.AnalysisPeriod.Hours() / 24

	for _, ch := range channels {
		if !managedChannels[ch.ChanId] {
			continue
		}

		if err := a.adjustChannel(ctx, ch, analysisMap, analysisDays, targetRate); err != nil {
			alias := a.client.GetNodeAlias(ctx, ch.RemotePubkey)
			a.logger.Error("failed to adjust channel fee",
				zap.String("peer", alias),
				zap.Uint64("chan_id", ch.ChanId),
				zap.Error(err),
			)
		}
	}

	return nil
}

func buildThroughputMap(events []*lnrpc.ForwardingEvent, managed map[uint64]bool) map[uint64]*channelThroughput {
	tm := make(map[uint64]*channelThroughput)
	for _, e := range events {
		cid := e.ChanIdOut
		if !managed[cid] {
			continue
		}
		t, ok := tm[cid]
		if !ok {
			t = &channelThroughput{ChanID: cid}
			tm[cid] = t
		}
		t.AmtOutSat += int64(e.AmtOut)
		t.EventCount++
	}
	return tm
}

func (a *AutoFees) calculateTargetRate(tm map[uint64]*channelThroughput, days float64) float64 {
	if len(tm) == 0 || days <= 0 {
		return 0
	}

	sorted := make([]*channelThroughput, 0, len(tm))
	for _, t := range tm {
		sorted = append(sorted, t)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].AmtOutSat > sorted[j].AmtOutSat
	})

	n := a.cfg.TopPeersCount
	if n > len(sorted) {
		n = len(sorted)
	}

	var sum int64
	for i := 0; i < n; i++ {
		sum += sorted[i].AmtOutSat
	}
	return float64(sum) / float64(n) / days
}

func (a *AutoFees) adjustChannel(ctx context.Context, ch *lnrpc.Channel, analysisMap map[uint64]*channelThroughput, analysisDays, targetRate float64) error {
	alias := a.client.GetNodeAlias(ctx, ch.RemotePubkey)

	edge, err := a.client.GetChanInfo(ctx, ch.ChanId)
	if err != nil {
		return err
	}
	currentFee := currentOutboundFeePPM(edge, ch.RemotePubkey)

	var recentRate float64
	if t, ok := analysisMap[ch.ChanId]; ok && analysisDays > 0 {
		recentRate = float64(t.AmtOutSat) / analysisDays
	}

	localRatio := float64(ch.LocalBalance) / float64(ch.Capacity)
	newFee := calculateAutoFee(currentFee, recentRate, targetRate, localRatio, a.cfg)

	if localRatio < a.cfg.LowLiquidityThreshold && a.cfg.LiquidityScarcityBonus > 0 {
		a.logger.Debug("applying scarcity bonus",
			zap.String("peer", alias),
			zap.Float64("local_ratio", math.Round(localRatio*1000)/1000),
			zap.Int64("bonus_ppm", a.cfg.LiquidityScarcityBonus),
		)
	}

	if newFee == currentFee {
		a.logger.Debug("no fee change needed",
			zap.String("peer", alias),
			zap.Int64("fee_ppm", currentFee),
		)
		return nil
	}

	if a.dryRun {
		a.logger.Info("[dry-run] would adjust fee",
			zap.String("peer", alias),
			zap.Uint64("chan_id", ch.ChanId),
			zap.Int64("old_fee_ppm", currentFee),
			zap.Int64("new_fee_ppm", newFee),
			zap.Float64("recent_rate_sat_day", math.Round(recentRate*100)/100),
			zap.Float64("target_rate_sat_day", math.Round(targetRate*100)/100),
			zap.Float64("local_ratio", math.Round(localRatio*1000)/1000),
		)
		return nil
	}

	a.logger.Info("adjusting fee",
		zap.String("peer", alias),
		zap.Uint64("chan_id", ch.ChanId),
		zap.Int64("old_fee_ppm", currentFee),
		zap.Int64("new_fee_ppm", newFee),
		zap.Float64("recent_rate_sat_day", math.Round(recentRate*100)/100),
		zap.Float64("target_rate_sat_day", math.Round(targetRate*100)/100),
		zap.Float64("local_ratio", math.Round(localRatio*1000)/1000),
	)

	return a.client.UpdateChannelPolicy(ctx, ch.ChannelPoint, safeUint32(newFee), a.cfg.BaseFee, 0, 0, a.cfg.TimeLockDelta)
}

func calculateAutoFee(currentFee int64, recentRate, targetRate float64, localRatio float64, cfg config.AutoFeesConfig) int64 {
	newFee := currentFee
	if targetRate > 0 && recentRate > 0 {
		if recentRate > targetRate {
			newFee = currentFee + cfg.FeeIncrementPPM
		} else {
			newFee = currentFee - cfg.FeeIncrementPPM
		}
	}

	if localRatio < cfg.LowLiquidityThreshold && cfg.LiquidityScarcityBonus > 0 {
		newFee += cfg.LiquidityScarcityBonus
	}

	return clamp(newFee, cfg.MinFeePPM, cfg.MaxFeePPM)
}

func currentOutboundFeePPM(edge *lnrpc.ChannelEdge, remotePubkey string) int64 {
	if edge.Node1Pub == remotePubkey {
		if edge.Node2Policy != nil {
			return edge.Node2Policy.FeeRateMilliMsat
		}
	} else {
		if edge.Node1Policy != nil {
			return edge.Node1Policy.FeeRateMilliMsat
		}
	}
	return 0
}

func clamp(v, min, max int64) int64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func isExcluded(id uint64, list []uint64) bool {
	for _, ex := range list {
		if id == ex {
			return true
		}
	}
	return false
}

func safeUint32(v int64) uint32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}
