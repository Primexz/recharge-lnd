package fees

import (
	"context"
	"time"

	"github.com/primexz/recharge-lnd/internal/config"
	lndclient "github.com/primexz/recharge-lnd/internal/lnd"
	"go.uber.org/zap"
)

type Manager struct {
	cfg      *config.Config
	client   *lndclient.Client
	auto     *AutoFees
	static   *StaticPolicies
	logger   *zap.Logger
	dryRun   bool
	lastRun  time.Time
	excluded map[uint64]bool
}

func NewManager(cfg *config.Config, client *lndclient.Client, logger *zap.Logger, dryRun bool) *Manager {
	m := &Manager{
		cfg:    cfg,
		client: client,
		logger: logger,
		dryRun: dryRun,
	}

	if len(cfg.Policies) > 0 {
		m.static = NewStaticPolicies(cfg.Policies, client, logger.Named("static"), dryRun)
	}

	if cfg.AutoFees.Enabled {
		m.auto = NewAutoFees(cfg.AutoFees, client, logger.Named("autofees"), dryRun)
	}

	return m
}

func (m *Manager) RunLoop(ctx context.Context) {
	m.runStaticPolicies(ctx)
	m.runAutoFees(ctx)

	policyTicker := time.NewTicker(m.cfg.PolicyInterval)
	defer policyTicker.Stop()

	autoTicker := time.NewTicker(m.cfg.AutoFees.AdjustmentInterval)
	defer autoTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("fee manager stopped")
			return
		case <-policyTicker.C:
			m.runStaticPolicies(ctx)
		case <-autoTicker.C:
			m.runAutoFees(ctx)
		}
	}
}

func (m *Manager) runStaticPolicies(ctx context.Context) {
	if m.static == nil {
		return
	}

	channels, err := m.client.ListChannels(ctx)
	if err != nil {
		m.logger.Error("failed to list channels", zap.Error(err))
		return
	}

	matched, err := m.static.Run(ctx, channels)
	if err != nil {
		m.logger.Error("static policy run failed", zap.Error(err))
		return
	}

	m.excluded = matched
	m.logger.Info("static policies applied", zap.Int("matched", len(matched)))
}

func (m *Manager) runAutoFees(ctx context.Context) {
	if m.auto == nil {
		return
	}

	channels, err := m.client.ListChannels(ctx)
	if err != nil {
		m.logger.Error("failed to list channels", zap.Error(err))
		return
	}

	excluded := m.excluded
	if excluded == nil {
		excluded = make(map[uint64]bool)
	}

	if err := m.auto.Run(ctx, channels, excluded); err != nil {
		m.logger.Error("autofees run failed", zap.Error(err))
		return
	}

	m.lastRun = time.Now()
	m.logger.Info("autofees adjustment completed",
		zap.Int("total_channels", len(channels)),
		zap.Int("policy_managed", len(excluded)),
		zap.Int("autofee_managed", len(channels)-len(excluded)),
	)
}
