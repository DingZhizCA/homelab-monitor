package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	data := []byte("server:\n  url: http://127.0.0.1:8080\nagent:\n  device_id: n1\n  display_name: N1\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Monitoring.IdleInterval.Duration != time.Minute || config.Monitoring.ActiveInterval.Duration != 5*time.Second {
		t.Fatalf("unexpected intervals: %+v", config.Monitoring)
	}
}

func TestLoadConfigRejectsDisabledRequiredCollector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	data := []byte("server:\n  url: http://127.0.0.1:8080\nagent:\n  device_id: n1\n  display_name: N1\ncollectors:\n  cpu: false\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("disabled CPU collector was accepted")
	}
}
