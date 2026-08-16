package nft

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
)

type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "nft", args...).Output()
}

type Collector struct {
	runner Runner
}

func NewCollector() *Collector {
	return &Collector{runner: commandRunner{}}
}

func NewCollectorWithRunner(runner Runner) *Collector {
	return &Collector{runner: runner}
}

func (c *Collector) Snapshot(ctx context.Context) (map[string]uint64, error) {
	result := make(map[string]uint64)
	for _, target := range [][]string{
		{"-j", "list", "set", "ip", "perip4", "hitv4"},
		{"-j", "list", "set", "ip6", "perip6", "hitv6"},
	} {
		output, err := c.runner.Run(ctx, target...)
		if err != nil {
			return nil, fmt.Errorf("run nft %v: %w", target, err)
		}
		counters, err := ParseSet(output)
		if err != nil {
			return nil, fmt.Errorf("parse nft %v: %w", target, err)
		}
		for ip, bytes := range counters {
			result[ip] = bytes
		}
	}
	return result, nil
}

type document struct {
	NFTables []object `json:"nftables"`
}

type object struct {
	Set *setObject `json:"set,omitempty"`
}

type setObject struct {
	Elements []elementWrapper `json:"elem"`
}

type elementWrapper struct {
	Element element `json:"elem"`
}

type element struct {
	Value   string  `json:"val"`
	Counter counter `json:"counter"`
}

type counter struct {
	Bytes uint64 `json:"bytes"`
}

func ParseSet(data []byte) (map[string]uint64, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	result := make(map[string]uint64)
	for _, item := range doc.NFTables {
		if item.Set == nil {
			continue
		}
		for _, wrapper := range item.Set.Elements {
			addr, err := netip.ParseAddr(wrapper.Element.Value)
			if err != nil {
				continue
			}
			result[addr.String()] = wrapper.Element.Counter.Bytes
		}
	}
	return result, nil
}
