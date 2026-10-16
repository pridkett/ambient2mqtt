package main

import (
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// stuckToken never completes, like a publish to a broker that's gone.
type stuckToken struct{ mqtt.Token }

func (stuckToken) WaitTimeout(time.Duration) bool { return false }

type stuckClient struct {
	mqtt.Client
	published int
}

func (c *stuckClient) Publish(string, byte, bool, interface{}) mqtt.Token {
	c.published++
	return stuckToken{}
}

// A stuck broker costs one timeout per report, not one per topic.
func TestPublishStopsWhenStuck(t *testing.T) {
	stuck := &stuckClient{}
	saved := client
	client = stuck
	defer func() { client = saved }()

	mq := &publisher{ok: true}
	for range 5 {
		mq.publish("weather/ws-2902/tempf", 0, "61.2")
	}
	if stuck.published != 1 || mq.ok {
		t.Errorf("published %d times, ok = %v; want 1, false", stuck.published, mq.ok)
	}
}
