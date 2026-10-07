package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/cpupolicy"
	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
)

func runCPUConfig(args []string) error {
	fs := flag.NewFlagSet("cpu-config", flag.ContinueOnError)
	path := configFlag(fs)
	busy := fs.String("busy", "", "CPU IDs/ranges while busy, e.g. 4-7 (a bare 4 means any four CPUs)")
	idle := fs.String("idle", "", "CPU IDs/ranges while idle, e.g. 0-7")
	disable := fs.Bool("disable", false, "disable the dynamic policy; apply restores incus.cpu")
	list := fs.Bool("list", false, "show CPU IDs, physical cores and maximum frequency")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *list {
		out, err := sys.Output("lscpu", "-e=CPU,CORE,SOCKET,ONLINE,MAXMHZ")
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	}
	c, err := load(*path)
	if err != nil {
		return err
	}
	if *disable && (*busy != "" || *idle != "") {
		return fmt.Errorf("use --disable alone")
	}
	if *disable {
		c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs = "", ""
	} else if *busy != "" || *idle != "" {
		if *busy != "" {
			c.Incus.CPUPolicy.BusyCPUs = *busy
		}
		if *idle != "" {
			c.Incus.CPUPolicy.IdleCPUs = *idle
		}
	} else {
		fmt.Printf("Busy: %s\nIdle: %s\nConfig: %s\n", c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs, *path)
		fmt.Println("Use --list to see cores; --busy 4-7 --idle 0-7 to choose them.")
		return nil
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Incus.CPUPolicy.Enabled() {
		online, err := os.ReadFile("/sys/devices/system/cpu/online")
		if err != nil {
			return err
		}
		for _, selection := range []string{c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs} {
			if err := cpupolicy.ValidateSelection(selection, strings.TrimSpace(string(online))); err != nil {
				return err
			}
		}
	}
	if err := c.Save(*path); err != nil {
		return err
	}
	fmt.Printf("Saved %s. Run sudo xivstream apply --cpu-policy-only --config %s to activate on systemd.\n", *path, *path)
	return nil
}
