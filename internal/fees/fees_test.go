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

func TestProportionalFee(t *testing.T) {
	tests := []struct {
		name                      string
		ratio, minRatio, maxRatio float64
		minFee, maxFee            int64
		want                      int64
	}{
		{
			name:  "at min ratio -> max fee",
			ratio: 0.05, minRatio: 0.05, maxRatio: 0.20,
			minFee: 32, maxFee: 400,
			want: 400,
		},
		{
			name:  "at max ratio -> min fee",
			ratio: 0.20, minRatio: 0.05, maxRatio: 0.20,
			minFee: 32, maxFee: 400,
			want: 32,
		},
		{
			name:  "midpoint",
			ratio: 0.125, minRatio: 0.05, maxRatio: 0.20,
			minFee: 32, maxFee: 400,
			want: 216,
		},
		{
			name:  "below min ratio clamps to max fee",
			ratio: 0.0, minRatio: 0.05, maxRatio: 0.20,
			minFee: 32, maxFee: 400,
			want: 400,
		},
		{
			name:  "above max ratio clamps to min fee",
			ratio: 1.0, minRatio: 0.05, maxRatio: 0.20,
			minFee: 32, maxFee: 400,
			want: 32,
		},
		{
			name:  "equal ratios returns min fee",
			ratio: 0.5, minRatio: 0.5, maxRatio: 0.5,
			minFee: 10, maxFee: 100,
			want: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proportionalFee(tt.ratio, tt.minRatio, tt.maxRatio, tt.minFee, tt.maxFee)
			if got != tt.want {
				t.Errorf("proportionalFee(%v, %v, %v, %d, %d) = %d, want %d",
					tt.ratio, tt.minRatio, tt.maxRatio, tt.minFee, tt.maxFee, got, tt.want)
			}
		})
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

func TestCurrentInboundFee(t *testing.T) {
	edge := &lnrpc.ChannelEdge{
		Node1Pub:    "local",
		Node2Pub:    "remote",
		Node1Policy: &lnrpc.RoutingPolicy{InboundFeeRateMilliMsat: -32},
		Node2Policy: &lnrpc.RoutingPolicy{InboundFeeRateMilliMsat: -64},
	}

	if got := currentInboundFee(edge, "remote"); got != -32 {
		t.Errorf("expected -32, got %d", got)
	}
	if got := currentInboundFee(edge, "local"); got != -64 {
		t.Errorf("expected -64, got %d", got)
	}
}

func TestMatchChannel(t *testing.T) {
	policies := []config.PolicyConfig{
		{
			Name:          "extreme",
			MaxRatio:      0.05,
			Strategy:      "proportional",
			MinFeePPM:     32,
			MaxFeePPM:     1000,
			InboundFeePPM: -64,
		},
		{
			Name:          "discourage",
			MinRatio:      0.05,
			MaxRatio:      0.20,
			Strategy:      "proportional",
			MinFeePPM:     32,
			MaxFeePPM:     400,
			InboundFeePPM: -32,
		},
		{
			Name:          "encourage",
			MinRatio:      0.98,
			Strategy:      "static",
			FeePPM:        4,
			InboundFeePPM: 0,
		},
	}

	sp := &StaticPolicies{policies: policies}

	tests := []struct {
		name       string
		local, cap int64
		wantMatch  bool
		wantPolicy string
		wantFee    int64
	}{
		{
			name:  "ratio 0.02 matches extreme",
			local: 20, cap: 1000,
			wantMatch: true, wantPolicy: "extreme", wantFee: 613,
		},
		{
			name:  "ratio 0.10 matches discourage",
			local: 100, cap: 1000,
			wantMatch: true, wantPolicy: "discourage",
			wantFee: 277,
		},
		{
			name:  "ratio 0.99 matches encourage",
			local: 990, cap: 1000,
			wantMatch: true, wantPolicy: "encourage", wantFee: 4,
		},
		{
			name:  "ratio 0.50 matches nothing",
			local: 500, cap: 1000,
			wantMatch: false,
		},
		{
			name:  "zero capacity no match",
			local: 0, cap: 0,
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &lnrpc.Channel{
				ChanId:       1,
				LocalBalance: tt.local,
				Capacity:     tt.cap,
			}
			result, ok := sp.matchChannel(ch)
			if ok != tt.wantMatch {
				t.Fatalf("matchChannel() matched = %v, want %v", ok, tt.wantMatch)
			}
			if !ok {
				return
			}
			if result.PolicyName != tt.wantPolicy {
				t.Errorf("policy = %q, want %q", result.PolicyName, tt.wantPolicy)
			}
			if result.FeePPM != tt.wantFee {
				t.Errorf("fee_ppm = %d, want %d", result.FeePPM, tt.wantFee)
			}
		})
	}
}

func TestMatchChannelSpecificChannels(t *testing.T) {
	sp := &StaticPolicies{
		policies: []config.PolicyConfig{
			{
				Name:     "specific",
				Strategy: "static",
				FeePPM:   42,
				Channels: []uint64{99},
			},
		},
	}

	ch := &lnrpc.Channel{ChanId: 99, LocalBalance: 500, Capacity: 1000}
	result, ok := sp.matchChannel(ch)
	if !ok || result.FeePPM != 42 {
		t.Errorf("expected channel 99 to match with fee 42, got matched=%v fee=%d", ok, result.FeePPM)
	}

	ch2 := &lnrpc.Channel{ChanId: 100, LocalBalance: 500, Capacity: 1000}
	_, ok = sp.matchChannel(ch2)
	if ok {
		t.Error("expected channel 100 to not match")
	}
}

func TestMatchChannelFirstRuleWins(t *testing.T) {
	sp := &StaticPolicies{
		policies: []config.PolicyConfig{
			{Name: "first", MaxRatio: 0.10, Strategy: "static", FeePPM: 100},
			{Name: "second", MaxRatio: 0.20, Strategy: "static", FeePPM: 200},
		},
	}

	ch := &lnrpc.Channel{ChanId: 1, LocalBalance: 50, Capacity: 1000}
	result, ok := sp.matchChannel(ch)
	if !ok || result.PolicyName != "first" {
		t.Errorf("expected first rule to win, got %q", result.PolicyName)
	}
}
