//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The lab's LAN is TEST-NET-2, which real networks never route. Nodes live
// in network namespaces bridged together inside one more namespace, so the
// host's firewall never sees their traffic. The host reaches the LAN at .254.
const (
	lanNS   = "wgl-lan"
	lanNet  = "198.51.100."
	lanHost = lanNet + "254"
)

// lab is a LAN of throwaway nodes and an inventory that describes them.
type lab struct {
	t     *testing.T
	dir   string // holds wglink.yaml and wglink.state.json
	work  string // node files, wrappers and ssh keys
	env   *[]string
	nodes map[string]*machine
	seen  *bytes.Buffer // everything wglink printed, to check for leaked keys
}

// machine is one node in the lab.
type machine struct {
	name  string
	lan   string   // its LAN address
	run   []string // runs a command as root on it
	wgDir string   // its /etc/wireguard, as the host sees it
}

func newLab(t *testing.T) *lab {
	l := &lab{t: t, dir: t.TempDir(), work: t.TempDir(), env: &[]string{}, nodes: map[string]*machine{}, seen: &bytes.Buffer{}}
	l.removeNetns(lanNS)
	l.root("ip", "netns", "add", lanNS)
	t.Cleanup(func() { l.removeNetns(lanNS) })
	l.root("ip", "-n", lanNS, "link", "add", "br0", "type", "bridge")
	l.root("ip", "-n", lanNS, "link", "set", "dev", "br0", "up")
	l.tryRoot("ip", "link", "del", "wgl-host")
	l.root("ip", "link", "add", "wgl-host", "type", "veth", "peer", "name", "host", "netns", lanNS)
	l.root("ip", "-n", lanNS, "link", "set", "dev", "host", "master", "br0", "up")
	l.root("ip", "addr", "add", lanHost+"/24", "dev", "wgl-host")
	l.root("ip", "link", "set", "dev", "wgl-host", "up")
	l.wglink("init")
	return l
}

// in returns the lab reporting to a subtest.
func (l *lab) in(t *testing.T) *lab {
	c := *l
	c.t = t
	return &c
}

// sudo is the prefix that runs a host command as root.
func sudo() []string {
	if os.Geteuid() == 0 {
		return nil
	}
	return []string{"sudo", "-n"}
}

// root runs a command as root on the host and fails the test if it fails.
func (l *lab) root(argv ...string) string {
	l.t.Helper()
	out, err := l.tryRoot(argv...)
	if err != nil {
		l.t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return out
}

func (l *lab) tryRoot(argv ...string) (string, error) {
	argv = append(sudo(), argv...)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}

// removeNetns kills whatever runs in a namespace, then deletes it.
func (l *lab) removeNetns(ns string) {
	if pids, err := l.tryRoot("ip", "netns", "pids", ns); err == nil {
		for _, pid := range strings.Fields(pids) {
			l.tryRoot("kill", "-9", pid)
		}
	}
	l.tryRoot("ip", "netns", "del", ns)
}

// netnsNode adds a node in its own network namespace at lanNet.N, with
// private /etc/wireguard and /run/wireguard (wireguard-go keeps its sockets
// there), reached through an exec wrapper.
func (l *lab) netnsNode(name string, n int) *machine {
	l.t.Helper()
	ns := "wgl-" + name
	l.removeNetns(ns)
	l.root("ip", "netns", "add", ns)
	l.t.Cleanup(func() { l.removeNetns(ns) })
	l.root("ip", "link", "add", "eth0", "netns", ns, "type", "veth", "peer", "name", name, "netns", lanNS)
	l.root("ip", "-n", lanNS, "link", "set", "dev", name, "master", "br0", "up")
	lan := fmt.Sprintf("%s%d", lanNet, n)
	l.root("ip", "-n", ns, "addr", "add", lan+"/24", "dev", "eth0")
	l.root("ip", "-n", ns, "link", "set", "dev", "eth0", "up")
	l.root("ip", "-n", ns, "link", "set", "dev", "lo", "up")

	home := filepath.Join(l.work, name)
	for _, d := range []string{"etc", "run"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			l.t.Fatal(err)
		}
	}
	wrapper := filepath.Join(home, "node.sh")
	script := fmt.Sprintf(`#!/bin/sh
# Runs a command as root on node %s.
exec %s ip netns exec %s sh -c '
mkdir -p /etc/wireguard /run/wireguard /run/sshd
mount --bind "$0/etc" /etc/wireguard
mount --bind "$0/run" /run/wireguard
exec "$@"' %s "$@"
`, name, strings.Join(sudo(), " "), ns, home)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		l.t.Fatal(err)
	}
	nd := &machine{name: name, lan: lan, run: []string{wrapper}, wgDir: filepath.Join(home, "etc")}
	l.nodes[name] = nd
	return nd
}

// addExecNode adds a namespace node to the inventory, reached through its wrapper.
func (l *lab) addExecNode(name string, n int) {
	nd := l.netnsNode(name, n)
	l.wglink("node", "add", name, "--exec", nd.run[0], "--endpoint", nd.lan, "--service", "none")
}

// addSSHNode adds a namespace node running its own sshd, reached over SSH as
// root. Its endpoint comes from the SSH destination.
func (l *lab) addSSHNode(name string, n int) {
	l.t.Helper()
	nd := l.netnsNode(name, n)
	home := filepath.Join(l.work, name)
	id := filepath.Join(l.work, "id_ed25519")
	if _, err := os.Stat(id); err != nil {
		l.run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "wglink-test", "-f", id)
	}
	l.run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", filepath.Join(home, "host_key"))
	pub, err := os.ReadFile(id + ".pub")
	if err != nil {
		l.t.Fatal(err)
	}
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			l.t.Fatal(err)
		}
	}
	write(filepath.Join(l.work, "authorized_keys"), string(pub))
	write(filepath.Join(home, "sshd_config"), fmt.Sprintf(`ListenAddress %s:22
HostKey %s/host_key
PidFile %s/sshd.pid
AuthorizedKeysFile %s/authorized_keys
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
`, nd.lan, home, home, l.work))
	cfg := filepath.Join(l.work, "ssh_config")
	write(cfg, fmt.Sprintf(`Host *
    User root
    IdentityFile %s
    IdentitiesOnly yes
    UserKnownHostsFile %s/known_hosts
    StrictHostKeyChecking accept-new
`, id, l.work))
	*l.env = append(*l.env, "WGLINK_SSH_CONFIG="+cfg)

	sshd := exec.Command(nd.run[0], "/usr/sbin/sshd", "-D", "-e", "-f", filepath.Join(home, "sshd_config"))
	var log bytes.Buffer
	sshd.Stdout, sshd.Stderr = &log, &log
	if err := sshd.Start(); err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() {
		l.removeNetns("wgl-" + name)
		sshd.Wait()
	})
	for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", nd.lan+":22", time.Second); err == nil {
			c.Close()
			break
		}
		if time.Since(start) > 10*time.Second {
			l.t.Fatalf("sshd on %s didn't start:\n%s", name, log.String())
		}
	}
	l.wglink("node", "add", name, "--ssh", "root@"+nd.lan, "--service", "none")
}

// addHostNode adds the machine running the tests as a local node that uses
// systemd, when WGLINK_INTEGRATION_HOST=1 allows it.
func (l *lab) addHostNode(name, iface string) bool {
	if os.Getenv("WGLINK_INTEGRATION_HOST") != "1" {
		return false
	}
	nd := &machine{name: name, lan: lanHost, run: sudo(), wgDir: "/etc/wireguard"}
	l.nodes[name] = nd
	cleanup := func() {
		l.tryRoot("systemctl", "disable", "--now", "wg-quick@"+iface)
		l.tryRoot("ip", "link", "del", iface)
		l.tryRoot("sh", "-c", "rm -f /etc/wireguard/"+iface+".*")
	}
	cleanup()
	l.t.Cleanup(cleanup)
	l.wglink("node", "add", name, "--local", "--sudo", "--endpoint", lanHost, "--service", "systemd")
	return true
}

// run runs a host command as the test user.
func (l *lab) run(argv ...string) string {
	l.t.Helper()
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		l.t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return string(out)
}

// wglink runs wglink on the lab's inventory and fails the test if it fails.
func (l *lab) wglink(args ...string) string {
	l.t.Helper()
	out, err := l.tryWglink(args...)
	if err != nil {
		l.t.Fatalf("wglink %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (l *lab) tryWglink(args ...string) (string, error) {
	cmd := exec.Command(bin, append([]string{"--dir", l.dir}, args...)...)
	cmd.Env = append(os.Environ(), *l.env...)
	out, err := cmd.CombinedOutput()
	l.seen.Write(out)
	return string(out), err
}

// on runs a command as root on a node.
func (l *lab) on(name string, argv ...string) (string, error) {
	argv = append(append([]string{}, l.nodes[name].run...), argv...)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}

// mustOn is on, failing the test if the command fails.
func (l *lab) mustOn(name string, argv ...string) string {
	l.t.Helper()
	out, err := l.on(name, argv...)
	if err != nil {
		l.t.Fatalf("on %s: %s: %v\n%s", name, strings.Join(argv, " "), err, out)
	}
	return out
}

// ping checks that from reaches addr, allowing a few seconds for handshakes.
func (l *lab) ping(from, addr string) {
	l.t.Helper()
	var out string
	var err error
	for range 5 {
		if out, err = l.on(from, "ping", "-c", "1", "-W", "1", addr); err == nil {
			return
		}
	}
	l.t.Errorf("%s can't ping %s: %v\n%s", from, addr, err, out)
}

// nodeFile reads a file in a node's /etc/wireguard.
func (l *lab) nodeFile(name, file string) string {
	l.t.Helper()
	out, err := l.tryRoot("cat", filepath.Join(l.nodes[name].wgDir, file))
	if err != nil {
		l.t.Fatalf("reading %s on %s: %v\n%s", file, name, err, out)
	}
	return out
}

// ifindex returns an interface's index on a node, which changes when it's recreated.
func (l *lab) ifindex(name, iface string) string {
	l.t.Helper()
	out := l.mustOn(name, "cat", "/sys/class/net/"+iface+"/ifindex")
	return strings.TrimSpace(out)
}

// addrs reads the tunnel addresses wglink recorded for a network.
func (l *lab) addrs(network string) map[string]string {
	l.t.Helper()
	var st struct {
		Networks map[string]struct {
			Members map[string]struct {
				IP string `json:"ip"`
			} `json:"members"`
		} `json:"networks"`
	}
	data, err := os.ReadFile(filepath.Join(l.dir, "wglink.state.json"))
	if err != nil {
		l.t.Fatal(err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		l.t.Fatal(err)
	}
	out := map[string]string{}
	for name, m := range st.Networks[network].Members {
		out[name] = m.IP
	}
	return out
}
