package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllowCommand(t *testing.T) {
	for _, command := range []string{"restart", "reload", "enable", "disable", "r", "l", "e", "d"} {
		assert.True(t, allowCommand(command))
	}
	for _, command := range []string{"", "destroy", "configure", "restartx"} {
		assert.False(t, allowCommand(command))
	}
}

func TestProtocolCommand(t *testing.T) {
	testCases := []struct {
		args     []string
		message  string
		expected string
	}{
		{[]string{"restart", "AS65001_PEER_v4"}, "", "restart AS65001_PEER_v4"},
		{[]string{"r", "AS65001_PEER_v4"}, "", "restart AS65001_PEER_v4"},
		{[]string{"reload", "all"}, "", "reload all"},
		{[]string{"l", "all"}, "", "reload all"},
		{[]string{"enable", "AS65001_PEER_v4"}, "", "enable AS65001_PEER_v4"},
		{[]string{"e", "AS65001_PEER_v4"}, "", "enable AS65001_PEER_v4"},
		{[]string{"disable", "AS65001_PEER_v4"}, "", `disable AS65001_PEER_v4 "Protocol manually disabled by pathvector"`},
		{[]string{"d", "AS65001_PEER_v4"}, "", `disable AS65001_PEER_v4 "Protocol manually disabled by pathvector"`},
		{[]string{"disable", "AS65001_PEER_v4"}, "maintenance", `disable AS65001_PEER_v4 "maintenance"`},
		{[]string{"enable", "AS65001_PEER_v4"}, "ignored", "enable AS65001_PEER_v4"},
	}
	for _, tc := range testCases {
		assert.Equal(t, tc.expected, protocolCommand(tc.args, tc.message))
	}
}
