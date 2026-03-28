package config

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	LND      LNDConfig      `mapstructure:"lnd"`
	AutoFees AutoFeesConfig `mapstructure:"autofees"`
	Policies []PolicyConfig `mapstructure:"policies"`
	LogLevel string         `mapstructure:"log_level"`
}

type LNDConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	TLSCertPath  string `mapstructure:"tls_cert_path"`
	MacaroonPath string `mapstructure:"macaroon_path"`
}

type AutoFeesConfig struct {
	Enabled            bool          `mapstructure:"enabled"`
	ReferencePeriod    time.Duration `mapstructure:"reference_period"`
	AnalysisPeriod     time.Duration `mapstructure:"analysis_period"`
	AdjustmentInterval time.Duration `mapstructure:"adjustment_interval"`
	TopPeersCount      int           `mapstructure:"top_peers_count"`
	FeeIncrementPPM    int64         `mapstructure:"fee_increment_ppm"`
	MinFeePPM          int64         `mapstructure:"min_fee_ppm"`
	MaxFeePPM          int64         `mapstructure:"max_fee_ppm"`
	LowLiquidityThreshold  float64  `mapstructure:"low_liquidity_threshold"`
	LiquidityScarcityBonus int64    `mapstructure:"liquidity_scarcity_bonus_ppm"`
	BaseFee            int64         `mapstructure:"base_fee_msat"`
	TimeLockDelta      uint32        `mapstructure:"time_lock_delta"`
	ExcludeChannels    []uint64      `mapstructure:"exclude_channels"`
}

type PolicyConfig struct {
	Name           string  `mapstructure:"name"`
	MinRatio       float64 `mapstructure:"min_ratio"`
	MaxRatio       float64 `mapstructure:"max_ratio"`
	Strategy       string  `mapstructure:"strategy"` // "static" or "proportional"
	FeePPM         int64   `mapstructure:"fee_ppm"`
	MinFeePPM      int64   `mapstructure:"min_fee_ppm"`
	MaxFeePPM      int64   `mapstructure:"max_fee_ppm"`
	BaseFee        int64   `mapstructure:"base_fee_msat"`
	InboundFeePPM  int32   `mapstructure:"inbound_fee_ppm"`
	InboundBaseFee int32   `mapstructure:"inbound_base_fee_msat"`
	TimeLockDelta  uint32  `mapstructure:"time_lock_delta"`
	Channels       []uint64 `mapstructure:"channels"`
}

func Load(path string) (*Config, error) {
	v := viper.New()
	setDefaults(v)

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.lnd-fees")
		v.AddConfigPath("/etc/lnd-fees")
	}

	v.SetEnvPrefix("LNDFEES")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
		if path != "" {
			return nil, fmt.Errorf("config file not found: %s", path)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("lnd.host", "localhost")
	v.SetDefault("lnd.port", 10009)
	v.SetDefault("lnd.tls_cert_path", expandHome("~/.lnd/tls.cert"))
	v.SetDefault("lnd.macaroon_path", expandHome("~/.lnd/data/chain/bitcoin/mainnet/admin.macaroon"))

	v.SetDefault("autofees.enabled", true)
	v.SetDefault("autofees.reference_period", "1440h")
	v.SetDefault("autofees.analysis_period", "168h")
	v.SetDefault("autofees.adjustment_interval", "72h")
	v.SetDefault("autofees.top_peers_count", 5)
	v.SetDefault("autofees.fee_increment_ppm", 5)
	v.SetDefault("autofees.min_fee_ppm", 1)
	v.SetDefault("autofees.max_fee_ppm", 5000)
	v.SetDefault("autofees.low_liquidity_threshold", 0.125)
	v.SetDefault("autofees.liquidity_scarcity_bonus_ppm", 10)
	v.SetDefault("autofees.base_fee_msat", 0)
	v.SetDefault("autofees.time_lock_delta", 40)

	v.SetDefault("log_level", "info")
}

func validate(cfg *Config) error {
	if cfg.LND.TLSCertPath == "" {
		return fmt.Errorf("lnd.tls_cert_path is required")
	}
	if cfg.LND.MacaroonPath == "" {
		return fmt.Errorf("lnd.macaroon_path is required")
	}
	if cfg.AutoFees.AnalysisPeriod > cfg.AutoFees.ReferencePeriod {
		return fmt.Errorf("autofees.analysis_period (%s) must be <= reference_period (%s)", cfg.AutoFees.AnalysisPeriod, cfg.AutoFees.ReferencePeriod)
	}
	if cfg.AutoFees.MinFeePPM > cfg.AutoFees.MaxFeePPM {
		return fmt.Errorf("autofees.min_fee_ppm (%d) must be <= max_fee_ppm (%d)", cfg.AutoFees.MinFeePPM, cfg.AutoFees.MaxFeePPM)
	}
	for i, p := range cfg.Policies {
		if p.Strategy != "static" && p.Strategy != "proportional" {
			return fmt.Errorf("policies[%d] (%s): strategy must be 'static' or 'proportional', got '%s'", i, p.Name, p.Strategy)
		}
		if p.MinRatio < 0 || p.MinRatio > 1 {
			return fmt.Errorf("policies[%d] (%s): min_ratio must be between 0 and 1", i, p.Name)
		}
		if p.MaxRatio < 0 || p.MaxRatio > 1 {
			return fmt.Errorf("policies[%d] (%s): max_ratio must be between 0 and 1", i, p.Name)
		}
		if p.MinRatio > p.MaxRatio && p.MaxRatio != 0 {
			return fmt.Errorf("policies[%d] (%s): min_ratio must be <= max_ratio", i, p.Name)
		}
		if p.Strategy == "proportional" && p.MinFeePPM > p.MaxFeePPM {
			return fmt.Errorf("policies[%d] (%s): min_fee_ppm must be <= max_fee_ppm for proportional strategy", i, p.Name)
		}
	}
	return nil
}

func expandHome(path string) string {
	if len(path) > 1 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home + path[1:]
	}
	return path
}
