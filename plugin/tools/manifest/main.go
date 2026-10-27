// Command manifest writes a copy of the plugin manifest that may only reach
// the given bridge hosts on the local network, instead of all private address
// ranges.
//
//	go run ./tools/manifest -hosts presence.example.com,192.0.2.16 -out build/manifest.json
//
// Navidrome only lets a plugin connect to a private address when the host
// name and the address (or a range containing it) are both listed, so list
// the bridge's host name and the IP it resolves to.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
)

func main() {
	in := flag.String("in", "manifest.json", "source manifest")
	out := flag.String("out", "build/manifest.json", "manifest to write")
	hosts := flag.String("hosts", "", "comma-separated bridge host names and IP addresses")
	flag.Parse()

	if err := run(*in, *out, splitList(*hosts)); err != nil {
		fmt.Fprintln(os.Stderr, "manifest:", err)
		os.Exit(1)
	}
}

func run(in, out string, hosts []string) error {
	if len(hosts) == 0 {
		return errors.New("-hosts is required")
	}
	if !containsIP(hosts) {
		fmt.Fprintln(os.Stderr, "manifest: warning: no IP address given; a bridge on a private address also needs its IP listed")
	}

	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("reading %s: %w", in, err)
	}
	if err := replacePrivateHosts(manifest, hosts); err != nil {
		return err
	}

	result, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(result, '\n'), 0o644)
}

// replacePrivateHosts swaps the private address ranges in the HTTP
// permission for the given hosts and keeps every public host.
func replacePrivateHosts(manifest map[string]any, hosts []string) error {
	http, ok := lookup(manifest, "permissions", "http").(map[string]any)
	if !ok {
		return errors.New("manifest has no permissions.http section")
	}
	current, ok := http["requiredHosts"].([]any)
	if !ok {
		return errors.New("manifest has no permissions.http.requiredHosts list")
	}

	updated := make([]any, 0, len(current)+len(hosts))
	for _, h := range hosts {
		updated = append(updated, h)
	}
	for _, entry := range current {
		if s, ok := entry.(string); ok && isPrivateRange(s) {
			continue
		}
		updated = append(updated, entry)
	}
	http["requiredHosts"] = updated
	return nil
}

func lookup(m map[string]any, keys ...string) any {
	var current any = m
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

func isPrivateRange(entry string) bool {
	ip, network, err := net.ParseCIDR(entry)
	if err != nil {
		return false
	}
	ones, _ := network.Mask.Size()
	return ones < 32 && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func containsIP(hosts []string) bool {
	for _, h := range hosts {
		if net.ParseIP(h) != nil {
			return true
		}
	}
	return false
}

func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
