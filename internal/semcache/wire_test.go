package semcache

import (
	"context"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/embed"
)

func TestFromOptionsDisabledByDefault(t *testing.T) {
	c := FromOptions(Options{}, newFakeStore())
	if c.cfg.Enabled {
		t.Error("zero Options produced an enabled cache")
	}
}

func TestFromOptionsAppliesDefaults(t *testing.T) {
	c := FromOptions(Options{Enabled: true}, newFakeStore())
	if c.cfg.Threshold != DefaultThreshold {
		t.Errorf("Threshold = %v, want %v", c.cfg.Threshold, DefaultThreshold)
	}
	if c.cfg.MaxEntries != DefaultMaxEntries {
		t.Errorf("MaxEntries = %d, want %d", c.cfg.MaxEntries, DefaultMaxEntries)
	}
	if c.cfg.Model != embed.DefaultModel {
		t.Errorf("Model = %q, want %q", c.cfg.Model, embed.DefaultModel)
	}
}

func TestFromOptionsKeepsExplicitValues(t *testing.T) {
	c := FromOptions(Options{
		Enabled:    true,
		Endpoint:   "http://127.0.0.1:9999",
		Model:      "custom-model",
		Threshold:  0.5,
		MaxEntries: 7,
		Timeout:    time.Second,
	}, newFakeStore())

	if !c.cfg.Enabled {
		t.Error("Enabled = false, want true")
	}
	if c.cfg.Model != "custom-model" {
		t.Errorf("Model = %q, want custom-model", c.cfg.Model)
	}
	if c.cfg.Threshold != 0.5 {
		t.Errorf("Threshold = %v, want 0.5", c.cfg.Threshold)
	}
	if c.cfg.MaxEntries != 7 {
		t.Errorf("MaxEntries = %d, want 7", c.cfg.MaxEntries)
	}

	cl, ok := c.embed.(*embed.Client)
	if !ok {
		t.Fatalf("embedder = %T, want *embed.Client", c.embed)
	}
	if cl.Model != "custom-model" {
		t.Errorf("client Model = %q, want custom-model", cl.Model)
	}
	if cl.Endpoint != "http://127.0.0.1:9999" {
		t.Errorf("client Endpoint = %q, want http://127.0.0.1:9999", cl.Endpoint)
	}
}

// With no model listening, every call must miss quietly rather than fail.
func TestFromOptionsMissesWithNoModelRunning(t *testing.T) {
	c := FromOptions(Options{
		Enabled:  true,
		Endpoint: "http://127.0.0.1:1", // reserved port, never listening
		Timeout:  200 * time.Millisecond,
	}, newFakeStore())

	c.Store(context.Background(), Request{Text: "hello"}, "the response")
	if _, ok := c.Lookup(context.Background(), Request{Text: "hello"}); ok {
		t.Error("hit with no model running")
	}
	if c.Len() != 0 {
		t.Errorf("index len = %d, want 0 with no model running", c.Len())
	}
}
