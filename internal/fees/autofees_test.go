package fees

import (
	"math"
	"testing"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/primexz/recharge-lnd/internal/config"
)

func defaultAutoFeesConfig() config.AutoFeesConfig {
	return config.AutoFeesConfig{
		FeeIncrementPPM:        5,
		MinFeePPM:              1,
		MaxFeePPM:              5000,
		LowLiquidityThreshold:  0.125,
		LiquidityScarcityBonus: 10,
		TopPeersCount:          3,
	}
}

func TestCalculateAutoFee(t *testing.T) {
	cfg := defaultAutoFeesConfig()

	tests := []struct {
		name       string
		currentFee int64
		recentRate float64
		targetRate float64
		localRatio float64
		want       int64
	}{
		{
			name:       "above target raises fee",
			currentFee: 100, recentRate: 200, targetRate: 100,
			localRatio: 0.5,
			want:       105,
		},
		{
			name:       "below target lowers fee",
			currentFee: 100, recentRate: 50, targetRate: 100,
			localRatio: 0.5,
			want:       95,
		},
		{
			name:       "zero target keeps fee unchanged",
			currentFee: 100, recentRate: 50, targetRate: 0,
			localRatio: 0.5,
			want:       100,
		},
		{
			name:       "scarcity bonus applied when low liquidity",
			currentFee: 100, recentRate: 50, targetRate: 100,
			localRatio: 0.05,
			want:       105, // 100 - 5 + 10
		},
		{
			name:       "scarcity bonus with above target",
			currentFee: 100, recentRate: 200, targetRate: 100,
			localRatio: 0.05,
			want:       115, // 100 + 5 + 10
		},
		{
			name:       "clamped to min",
			currentFee: 3, recentRate: 50, targetRate: 100,
			localRatio: 0.5,
			want:       1,
		},
		{
			name:       "clamped to max",
			currentFee: 4998, recentRate: 200, targetRate: 100,
			localRatio: 0.5,
			want:       5000,
		},
		{
			name:       "at exact threshold no scarcity bonus",
			currentFee: 100, recentRate: 50, targetRate: 100,
			localRatio: 0.125,
			want:       95, // 100 - 5, ratio not < threshold
		},
		{
			name:       "just below threshold gets scarcity bonus",
			currentFee: 100, recentRate: 50, targetRate: 100,
			localRatio: 0.124,
			want:       105, // 100 - 5 + 10
		},
		{
			name:       "equal rate and target lowers fee",
			currentFee: 100, recentRate: 100, targetRate: 100,
			localRatio: 0.5,
			want:       95, // not strictly greater, so lowers
		},
		{
			name:       "zero recent rate keeps fee unchanged",
			currentFee: 100, recentRate: 0, targetRate: 100,
			localRatio: 0.5,
			want:       100, // no history, don't penalize
		},
		{
			name:       "zero recent rate with low liquidity gets only scarcity bonus",
			currentFee: 100, recentRate: 0, targetRate: 100,
			localRatio: 0.05,
			want:       110, // 100 + 10 scarcity, no direction change
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateAutoFee(tt.currentFee, tt.recentRate, tt.targetRate, tt.localRatio, cfg)
			if got != tt.want {
				t.Errorf("calculateAutoFee() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCalculateAutoFeeNoScarcityBonus(t *testing.T) {
	cfg := defaultAutoFeesConfig()
	cfg.LiquidityScarcityBonus = 0

	got := calculateAutoFee(100, 50.0, 100.0, 0.05, cfg)
	if got != 95 {
		t.Errorf("expected 95 with zero scarcity bonus, got %d", got)
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		v, min, max, want int64
	}{
		{50, 1, 100, 50},
		{0, 1, 100, 1},
		{-10, 1, 100, 1},
		{200, 1, 100, 100},
		{1, 1, 100, 1},
		{100, 1, 100, 100},
	}
	for _, tt := range tests {
		got := clamp(tt.v, tt.min, tt.max)
		if got != tt.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", tt.v, tt.min, tt.max, got, tt.want)
		}
	}
}

func TestSafeUint32(t *testing.T) {
	tests := []struct {
		v    int64
		want uint32
	}{
		{0, 0},
		{100, 100},
		{-1, 0},
		{-1000, 0},
		{int64(math.MaxUint32), math.MaxUint32},
		{int64(math.MaxUint32) + 1, math.MaxUint32},
		{math.MaxInt64, math.MaxUint32},
	}
	for _, tt := range tests {
		got := safeUint32(tt.v)
		if got != tt.want {
			t.Errorf("safeUint32(%d) = %d, want %d", tt.v, got, tt.want)
		}
	}
}

func TestIsExcluded(t *testing.T) {
	list := []uint64{1, 5, 10}
	if !isExcluded(5, list) {
		t.Error("expected 5 to be excluded")
	}
	if isExcluded(6, list) {
		t.Error("expected 6 to not be excluded")
	}
	if isExcluded(1, nil) {
		t.Error("expected nil list to exclude nothing")
	}
}

func TestCalculateTargetRate(t *testing.T) {
	a := &AutoFees{cfg: config.AutoFeesConfig{TopPeersCount: 3}}

	tests := []struct {
		name string
		tm   map[uint64]*channelThroughput
		days float64
		want float64
	}{
		{
			name: "empty map",
			tm:   map[uint64]*channelThroughput{},
			days: 60,
			want: 0,
		},
		{
			name: "fewer peers than top count",
			tm: map[uint64]*channelThroughput{
				1: {AmtOutSat: 6000},
				2: {AmtOutSat: 12000},
			},
			days: 60,
			want: 150, // avg(12000, 6000) / 60 = 150
		},
		{
			name: "more peers than top count",
			tm: map[uint64]*channelThroughput{
				1: {AmtOutSat: 30000},
				2: {AmtOutSat: 18000},
				3: {AmtOutSat: 6000},
				4: {AmtOutSat: 3000},
				5: {AmtOutSat: 600},
			},
			days: 60,
			want: 300, // avg(30000, 18000, 6000) / 60 = 300
		},
		{
			name: "single peer",
			tm: map[uint64]*channelThroughput{
				1: {AmtOutSat: 7000},
			},
			days: 7,
			want: 1000, // 7000 / 7
		},
		{
			name: "zero days returns zero",
			tm: map[uint64]*channelThroughput{
				1: {AmtOutSat: 1000},
			},
			days: 0,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.calculateTargetRate(tt.tm, tt.days)
			if got != tt.want {
				t.Errorf("calculateTargetRate() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestCurrentOutboundFeePPM(t *testing.T) {
	edge := &lnrpc.ChannelEdge{
		Node1Pub:    "local_pub",
		Node2Pub:    "remote_pub",
		Node1Policy: &lnrpc.RoutingPolicy{FeeRateMilliMsat: 100},
		Node2Policy: &lnrpc.RoutingPolicy{FeeRateMilliMsat: 200},
	}

	// remote is Node2 -> our policy is Node1
	if got := currentOutboundFeePPM(edge, "remote_pub"); got != 100 {
		t.Errorf("expected our fee (Node1) = 100, got %d", got)
	}

	// remote is Node1 -> our policy is Node2
	if got := currentOutboundFeePPM(edge, "local_pub"); got != 200 {
		t.Errorf("expected our fee (Node2) = 200, got %d", got)
	}

	// nil policy returns 0
	nilEdge := &lnrpc.ChannelEdge{Node1Pub: "a", Node2Pub: "b"}
	if got := currentOutboundFeePPM(nilEdge, "b"); got != 0 {
		t.Errorf("expected 0 for nil policy, got %d", got)
	}
}
