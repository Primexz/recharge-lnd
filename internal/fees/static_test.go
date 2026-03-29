package fees

import (
	"testing"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/primexz/recharge-lnd/internal/config"
)

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
