package process

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"

	"github.com/natesales/pathvector/pkg/bird"
	"github.com/natesales/pathvector/pkg/embed"
	"github.com/natesales/pathvector/pkg/templating"
)

// renderConfig loads a YAML config, renders the global and peer templates into a temporary directory and,
// if a BIRD binary is available, validates the result with `bird -p`. It returns the rendered files keyed by peer name ("" for the global config).
func renderConfig(t *testing.T, configYAML string) map[string]string {
	t.Helper()
	c, err := Load([]byte(configYAML))
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if err := templating.Load(embed.FS); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	out := map[string]string{}

	var global bytes.Buffer
	if err := templating.Template.ExecuteTemplate(&global, "global.tmpl", c); err != nil {
		t.Fatalf("global template: %v", err)
	}
	out[""] = global.String()
	if err := os.WriteFile(path.Join(dir, "bird.conf"), global.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	for name, p := range c.Peers {
		var b bytes.Buffer
		if err := templating.Template.ExecuteTemplate(&b, "peer.tmpl", &templating.Wrapper{Name: name, Peer: *p, Config: *c}); err != nil {
			t.Fatalf("peer template: %v", err)
		}
		out[name] = bird.Reformat(b.String())
		if err := os.WriteFile(path.Join(dir, fmt.Sprintf("AS%d_%s.conf", *p.ASN, *p.ProtocolName)), []byte(out[name]), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if birdBin, err := exec.LookPath("bird"); err == nil {
		cmd := exec.Command(birdBin, "-p", "-c", "bird.conf")
		cmd.Dir = dir
		if o, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("BIRD validation failed: %v\n%s", err, o)
		}
	} else {
		t.Log("bird binary not found, skipping BIRD validation")
	}

	return out
}

// protocolLevel returns the part of a rendered peer config outside of channel ({ipv4,ipv6} { ... }) blocks
func protocolLevel(conf string) string {
	var out strings.Builder
	depth := 0
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 1 && (strings.HasPrefix(trimmed, "ipv4 {") || strings.HasPrefix(trimmed, "ipv6 {")) {
			depth = 2
			continue
		}
		if depth == 0 && strings.HasPrefix(trimmed, "protocol bgp") {
			depth = 1
			continue
		}
		if depth == 1 {
			if trimmed == "}" {
				depth = 0
				continue
			}
			out.WriteString(trimmed + "\n")
			continue
		}
		if depth >= 2 {
			depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			if depth < 2 {
				depth = 1
			}
		}
	}
	return out.String()
}

func TestRenderAdvertiseHostname(t *testing.T) {
	out := renderConfig(t, `
asn: 65530
router-id: 192.0.2.1
hostname: router.example.com
rpki-enable: false
peers:
  AS112:
    asn: 112
    advertise-hostname: true
    neighbors: [192.0.2.112, 2001:db8::112]
`)
	// advertise hostname is a protocol option, it must not be rendered inside a channel block
	if !strings.Contains(protocolLevel(out["AS112"]), "advertise hostname on;") {
		t.Errorf("advertise hostname not found at protocol level:\n%s", out["AS112"])
	}
}
