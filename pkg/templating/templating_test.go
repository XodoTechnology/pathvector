package templating

import (
	"testing"

	"github.com/natesales/pathvector/pkg/config"
	"github.com/natesales/pathvector/pkg/embed"
)

func TestLoadTemplates(t *testing.T) {
	if err := Load(embed.FS); err != nil {
		t.Error(err)
	}
}

func TestWriteUIFile(t *testing.T) {
	WriteUIFile(&config.Config{WebUIFile: "/tmp/pathvector-go-test-ui.html"})
}

func TestWriteBlankVRRPConfig(t *testing.T) {
	WriteVRRPConfig(&config.Config{
		VRRPInstances:    map[string]*config.VRRPInstance{},
		KeepalivedConfig: "/tmp/pathvector-go-test-keepalived.conf",
	})
}

func TestWriteVRRPConfig(t *testing.T) {
	if err := WriteVRRPConfig(&config.Config{
		VRRPInstances:    map[string]*config.VRRPInstance{"VRRP 1": {State: "primary"}},
		KeepalivedConfig: "/tmp/pathvector-go-test-keepalived.conf",
	}); err != nil {
		t.Error(err)
	}
}
