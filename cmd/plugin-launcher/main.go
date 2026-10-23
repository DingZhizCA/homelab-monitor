//go:build linux

/*
Command plugin-launcher applies inherited Linux resource limits and then
replaces itself with one verified plugin artifact selected by the Agent.
*/
package main

import (
	"fmt"
	"os"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func main() {
	if err := plugins.RunPluginLauncher(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
