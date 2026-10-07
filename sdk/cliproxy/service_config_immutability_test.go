package cliproxy

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServiceCommitConfigUpdatePublishesPrivateSnapshot(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	incoming := &config.Config{
		SDKConfig: config.SDKConfig{ProxyURL: "http://reload.invalid"},
		OAuthExcludedModels: map[string][]string{
			"codex": {"hidden-model"},
		},
	}
	commit := service.commitConfigUpdate(incoming)
	if commit.cfg == nil {
		t.Fatal("commitConfigUpdate() rejected valid config")
	}
	if commit.cfg == incoming {
		t.Fatal("commitConfigUpdate() published caller-owned config pointer")
	}

	const iterations = 200
	var readers sync.WaitGroup
	var changed atomic.Bool
	readers.Add(1)
	go func() {
		defer readers.Done()
		for index := 0; index < iterations; index++ {
			service.cfgMu.RLock()
			proxyURL := service.cfg.ProxyURL
			excluded := service.cfg.OAuthExcludedModels["codex"][0]
			service.cfgMu.RUnlock()
			if proxyURL != "http://reload.invalid" || excluded != "hidden-model" {
				changed.Store(true)
			}
		}
	}()
	for index := 0; index < iterations; index++ {
		incoming.ProxyURL = "http://caller-mutated.invalid"
		incoming.OAuthExcludedModels["codex"][0] = "caller-mutated-model"
	}
	readers.Wait()
	if changed.Load() {
		t.Fatal("service runtime config changed when reload caller mutated its config")
	}
}
