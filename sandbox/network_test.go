package sandbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNetworkLinkToggle(t *testing.T) {
	net := NewNetwork()
	assert.True(t, net.Connected("node1", "node2"), "links start connected")

	net.SetLink("node1", "node2", false)
	assert.False(t, net.Connected("node1", "node2"))
	assert.False(t, net.Connected("node2", "node1"), "key is order-independent")

	net.SetLink("node2", "node1", true)
	assert.True(t, net.Connected("node1", "node2"))
}

func TestNetworkReconnect(t *testing.T) {
	net := NewNetwork()
	net.SetLink("a", "b", false)
	net.SetLink("c", "d", false)
	net.Reconnect()
	assert.True(t, net.Connected("a", "b"))
	assert.True(t, net.Connected("c", "d"))
}

func TestNetworkDelay(t *testing.T) {
	net := NewNetwork()
	assert.Equal(t, defaultDelay, net.Delay())
	net.SetDelay(250 * time.Millisecond)
	assert.Equal(t, 250*time.Millisecond, net.Delay())
}
