package agent

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	d.Duration = value
	return nil
}

type Config struct {
	Server struct {
		URL   string `yaml:"url"`
		Token string `yaml:"token"`
	} `yaml:"server"`
	Agent struct {
		DeviceID    string   `yaml:"device_id"`
		DisplayName string   `yaml:"display_name"`
		Interval    Duration `yaml:"interval"`
	} `yaml:"agent"`
	Monitoring struct {
		IdleInterval   Duration `yaml:"idle_interval"`
		ActiveInterval Duration `yaml:"active_interval"`
	} `yaml:"monitoring"`
	Collectors struct {
		CPU            *bool    `yaml:"cpu"`
		Memory         *bool    `yaml:"memory"`
		Disk           *bool    `yaml:"disk"`
		Network        *bool    `yaml:"network"`
		SensorInterval Duration `yaml:"sensor_interval"`
		DiskInterval   Duration `yaml:"disk_interval"`
	} `yaml:"collectors"`
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	var config Config
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if config.Agent.Interval.Duration == 0 {
		config.Agent.Interval.Duration = 5 * time.Second
	}
	if config.Monitoring.ActiveInterval.Duration == 0 {
		config.Monitoring.ActiveInterval.Duration = config.Agent.Interval.Duration
	}
	if config.Monitoring.IdleInterval.Duration == 0 {
		config.Monitoring.IdleInterval.Duration = time.Minute
	}
	if config.Collectors.SensorInterval.Duration == 0 {
		config.Collectors.SensorInterval.Duration = time.Minute
	}
	if config.Collectors.DiskInterval.Duration == 0 {
		config.Collectors.DiskInterval.Duration = 5 * time.Minute
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	parsed, err := url.Parse(c.Server.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("server.url must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(c.Agent.DeviceID) == "" {
		return fmt.Errorf("agent.device_id is required")
	}
	if strings.TrimSpace(c.Agent.DisplayName) == "" {
		return fmt.Errorf("agent.display_name is required")
	}
	if c.Agent.Interval.Duration <= 0 {
		return fmt.Errorf("agent.interval must be greater than zero")
	}
	if c.Monitoring.ActiveInterval.Duration < time.Second || c.Monitoring.IdleInterval.Duration < c.Monitoring.ActiveInterval.Duration {
		return fmt.Errorf("monitoring intervals must be positive and idle_interval must not be shorter than active_interval")
	}
	if !enabled(c.Collectors.CPU) || !enabled(c.Collectors.Memory) {
		return fmt.Errorf("CPU and memory collectors are required in phase 1")
	}
	if c.Collectors.SensorInterval.Duration < c.Monitoring.ActiveInterval.Duration {
		return fmt.Errorf("collectors.sensor_interval must not be shorter than monitoring.active_interval")
	}
	if c.Collectors.DiskInterval.Duration < c.Monitoring.ActiveInterval.Duration {
		return fmt.Errorf("collectors.disk_interval must not be shorter than monitoring.active_interval")
	}
	return nil
}

func enabled(value *bool) bool { return value == nil || *value }
