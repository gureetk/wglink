package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/engine"
	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/runner"
)

// statusTimeout bounds each node's answer; a dead node shouldn't stall status.
const statusTimeout = 30 * time.Second

// localNode is this machine, for status --local. Tests swap in a fake.
var localNode = node.Node{Runner: runner.Local{Sudo: true}}

func (a *app) newStatusCmd() *cobra.Command {
	var f struct {
		nets                        []string
		noPing, json, mustOK, local bool
	}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether each tunnel is up and passing traffic",
		Long: `Ask every member for its live WireGuard state and show each pair's link.
A link is ok after a handshake in the last 3 minutes or a ping reply through
the tunnel. wglink pings each peer's tunnel address unless --no-ping is set.

With --local, or when no wglink.yaml is found, status shows this machine's
WireGuard interfaces instead, naming peers from the configs wglink wrote.`,
		Example: `  wglink status
  wglink status --net lab --json
  wglink status --exit-code     # for scripts: 2 unless every link is ok
  sudo wglink status --local    # on a node, without wglink.yaml`,
		GroupID: "run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if !f.local {
				dir, err := config.Find(a.dir)
				switch {
				case errors.Is(err, config.ErrNotFound) && a.dir == "" && os.Getenv("WGLINK_DIR") == "" && len(f.nets) == 0:
					fmt.Fprintln(cmd.ErrOrStderr(), "No wglink.yaml found, so showing this machine's WireGuard interfaces.")
					f.local = true
				case err != nil:
					return err
				default:
					s, err := engine.Open(dir)
					if err != nil {
						return err
					}
					s.Timeout = statusTimeout
					r, err := s.Status(cmd.Context(), engine.StatusOptions{Networks: f.nets, NoPing: f.noPing})
					if err != nil {
						return err
					}
					if f.json {
						if err := writeJSON(out, reportJSON(r)); err != nil {
							return err
						}
					} else {
						engine.WriteStatus(out, r, style(out))
					}
					if f.mustOK && !r.AllOK() {
						return &exitCode{2}
					}
					return nil
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), statusTimeout)
			defer cancel()
			ifaces, err := engine.LocalStatus(ctx, localNode, !f.noPing)
			if err != nil {
				if os.Geteuid() != 0 && strings.Contains(err.Error(), "sudo") {
					return fmt.Errorf("%w\nhint: reading WireGuard state needs root; try: sudo wglink status --local", err)
				}
				return err
			}
			if f.json {
				if err := writeJSON(out, localJSON(ifaces)); err != nil {
					return err
				}
			} else {
				engine.WriteLocalStatus(out, ifaces, style(out))
			}
			if f.mustOK && !engine.LocalOK(ifaces) {
				return &exitCode{2}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringSliceVar(&f.nets, "net", nil, "only these networks (repeatable)")
	fl.BoolVar(&f.noPing, "no-ping", false, "judge links by handshakes alone")
	fl.BoolVar(&f.json, "json", false, "print JSON")
	fl.BoolVar(&f.mustOK, "exit-code", false, "exit with status 2 unless every link is ok")
	fl.BoolVar(&f.local, "local", false, "show this machine's interfaces instead of wglink.yaml's networks")
	cmd.MarkFlagsMutuallyExclusive("net", "local")
	return cmd
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// The JSON shapes below are wglink's stable output for scripts.

type statusOut struct {
	Networks    []netOut          `json:"networks"`
	Unreachable map[string]string `json:"unreachable,omitempty"`
}

type netOut struct {
	Name    string      `json:"name"`
	Iface   string      `json:"iface"`
	Subnet  string      `json:"subnet,omitempty"`
	Port    uint16      `json:"port,omitempty"`
	Members []memberOut `json:"members"`
	Links   []linkOut   `json:"links"`
}

type memberOut struct {
	Node    string `json:"node"`
	Address string `json:"address,omitempty"`
	State   string `json:"state"`
	Drift   bool   `json:"drift"`
}

type healthOut struct {
	State string `json:"state"`
	// Seconds since the latest handshake; null if there was none.
	HandshakeAge *int64 `json:"handshake_age_seconds"`
	// Ping round trip; null without a reply.
	RTT  *float64 `json:"rtt_ms"`
	RX   uint64   `json:"rx_bytes"`
	TX   uint64   `json:"tx_bytes"`
	Note string   `json:"note"`
}

type linkOut struct {
	A string `json:"a"`
	B string `json:"b"`
	healthOut
}

type localOut struct {
	Interfaces []ifaceOut `json:"interfaces"`
}

type ifaceOut struct {
	Name      string    `json:"name"`
	Network   string    `json:"network,omitempty"`
	Up        bool      `json:"up"`
	Addresses []string  `json:"addresses,omitempty"`
	Port      uint16    `json:"port,omitempty"`
	Peers     []peerOut `json:"peers"`
}

type peerOut struct {
	Name      string `json:"name,omitempty"`
	PublicKey string `json:"public_key"`
	Endpoint  string `json:"endpoint,omitempty"`
	Address   string `json:"address,omitempty"`
	healthOut
}

func reportJSON(r *engine.Report) statusOut {
	out := statusOut{Networks: []netOut{}}
	for _, n := range r.Networks {
		no := netOut{Name: n.Name, Iface: n.Iface, Port: n.Port, Members: []memberOut{}, Links: []linkOut{}}
		if n.Subnet.IsValid() {
			no.Subnet = n.Subnet.String()
		}
		for _, m := range n.Members {
			mo := memberOut{Node: m.Node, State: string(m.State), Drift: m.Drift}
			if m.Addr.IsValid() {
				mo.Address = m.Addr.String()
			}
			no.Members = append(no.Members, mo)
		}
		for _, l := range n.Links {
			no.Links = append(no.Links, linkOut{A: l.A, B: l.B, healthOut: healthJSON(l.Health)})
		}
		out.Networks = append(out.Networks, no)
	}
	if len(r.Errors) > 0 {
		out.Unreachable = map[string]string{}
		for name, err := range r.Errors {
			out.Unreachable[name] = err.Error()
		}
	}
	return out
}

func localJSON(ifaces []*engine.IfaceReport) localOut {
	out := localOut{Interfaces: []ifaceOut{}}
	for _, r := range ifaces {
		o := ifaceOut{Name: r.Name, Network: r.Network, Up: r.Up, Port: r.Port, Peers: []peerOut{}}
		for _, a := range r.Addresses {
			o.Addresses = append(o.Addresses, a.String())
		}
		for _, p := range r.Peers {
			po := peerOut{Name: p.Name, PublicKey: p.PublicKey, Endpoint: p.Endpoint, healthOut: healthJSON(p.Health)}
			if p.Addr.IsValid() {
				po.Address = p.Addr.String()
			}
			o.Peers = append(o.Peers, po)
		}
		out.Interfaces = append(out.Interfaces, o)
	}
	return out
}

func healthJSON(h engine.Health) healthOut {
	out := healthOut{State: string(h.State), RX: h.RX, TX: h.TX, Note: h.Note}
	if h.Handshake != engine.Never {
		age := int64(h.Handshake / time.Second)
		out.HandshakeAge = &age
	}
	if h.RTT > 0 {
		ms := float64(h.RTT) / float64(time.Millisecond)
		out.RTT = &ms
	}
	return out
}
