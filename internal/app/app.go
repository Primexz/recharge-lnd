package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/primexz/recharge-lnd/internal/config"
	"github.com/primexz/recharge-lnd/internal/fees"
	"github.com/primexz/recharge-lnd/internal/lnd"
	"go.uber.org/zap"
)

func Run(configPath, version string, dryRun bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger, err := buildLogger(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("creating logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	logger.Info("starting lnd-fees",
		zap.String("version", version),
		zap.Bool("autofees", cfg.AutoFees.Enabled),
		zap.Int("policies", len(cfg.Policies)),
		zap.Bool("dry_run", dryRun),
	)

	client := lnd.NewClient(cfg.LND.Host, cfg.LND.Port, cfg.LND.TLSCertPath, cfg.LND.MacaroonPath, logger.Named("lnd"))
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connecting to LND: %w", err)
	}
	defer func() { _ = client.Disconnect() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	info, err := client.GetInfo(ctx)
	if err != nil {
		return fmt.Errorf("getting node info: %w", err)
	}
	logger.Info("connected to LND node",
		zap.String("alias", info.Alias),
		zap.String("pubkey", info.IdentityPubkey),
		zap.Uint32("num_active_channels", info.NumActiveChannels),
		zap.String("version", info.Version),
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", zap.String("signal", sig.String()))
		cancel()
	}()

	manager := fees.NewManager(cfg, client, logger.Named("fees"), dryRun)
	manager.RunLoop(ctx)

	return nil
}

func buildLogger(level string) (*zap.Logger, error) {
	var zapCfg zap.Config

	switch level {
	case "debug":
		zapCfg = zap.NewDevelopmentConfig()
	case "warn":
		zapCfg = zap.NewProductionConfig()
		zapCfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		zapCfg = zap.NewProductionConfig()
		zapCfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		zapCfg = zap.NewProductionConfig()
		zapCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	return zapCfg.Build()
}
