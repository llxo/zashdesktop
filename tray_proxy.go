package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed build/windows/icon.ico
var appIcon []byte

const (
	defaultTrayAPIURL        = "http://127.0.0.1:9090"
	trayProxyRefreshInterval = 5 * time.Second
	maxTrayProxyResponse     = 8 << 20
)

type trayProxy struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

type trayProxyResponse struct {
	Proxies map[string]trayProxy `json:"proxies"`
}

type trayMenuState struct {
	CoreRunning   bool        `json:"coreRunning"`
	CoreInstalled bool        `json:"coreInstalled"`
	CoreType      string      `json:"coreType"`
	Groups        []trayProxy `json:"groups"`
}

var trayProxyHTTPClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		Proxy: nil,
	},
}

func (a *App) startTrayProxyRefresh() {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.trayProxyCancel = cancel
	a.mu.Unlock()

	go func() {
		a.refreshTrayProxyGroups(ctx)
		ticker := time.NewTicker(trayProxyRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refreshTrayProxyGroups(ctx)
			}
		}
	}()
}

func (a *App) refreshTrayProxyGroups(ctx context.Context) {
	a.trayProxyRefreshMu.Lock()
	defer a.trayProxyRefreshMu.Unlock()

	var coreConfig CoreConfig
	if a.coreService != nil {
		var err error
		coreConfig, err = a.coreService.GetConfig()
		if err != nil {
			debugLogf("tray", "refresh tray proxy groups get core config: %v", err)
		}
	}

	coreFingerprint := fmt.Sprintf("%t:%t:%s", coreConfig.Running, coreConfig.Installed, coreConfig.CoreType)

	var groups []trayProxy
	var responseHash string
	if coreConfig.Running {
		var err error
		groups, responseHash, err = a.fetchTrayProxyGroupsCached(ctx)
		if err != nil {
			debugLogf("tray", "refresh tray proxy groups: %v", err)
			groups = nil
			responseHash = ""
		}
	}

	fingerprint := coreFingerprint + "|" + responseHash

	a.mu.Lock()
	if a.quitting || fingerprint == a.trayProxyFingerprint {
		a.mu.Unlock()
		return
	}
	a.trayProxyFingerprint = fingerprint
	a.mu.Unlock()

	a.setTrayMenu(coreConfig, groups)
}

func (a *App) setTrayMenu(coreConfig CoreConfig, groups []trayProxy) {
	menu := a.app.Menu.New()
	menu.Add("打开 zashdesktop").OnClick(func(*application.Context) {
		a.showWindow()
	})
	menu.AddSeparator()
	clearCacheMenu := menu.AddSubmenu("清理前端缓存")
	clearCacheMenu.Add("确认清理").OnClick(func(*application.Context) {
		go a.clearFrontendCache()
	})

	menu.AddSeparator()
	if coreConfig.Running {
		menu.Add("停止核心").OnClick(func(*application.Context) {
			go a.trayStopCore()
		})
		restartItem := menu.Add("重启核心")
		restartItem.SetEnabled(coreConfig.Installed)
		restartItem.OnClick(func(*application.Context) {
			go a.trayRestartCore()
		})
	} else {
		startItem := menu.Add("启动核心")
		startItem.SetEnabled(coreConfig.Installed)
		startItem.OnClick(func(*application.Context) {
			go a.trayStartCore()
		})
		restartItem := menu.Add("重启核心")
		restartItem.SetEnabled(coreConfig.Installed)
		restartItem.OnClick(func(*application.Context) {
			go a.trayRestartCore()
		})
	}

	if len(groups) > 0 {
		proxyMenu := menu.AddSubmenu("代理组")
		for _, group := range groups {
			group := group
			groupMenu := proxyMenu.AddSubmenu(group.Name)
			selectable := strings.EqualFold(group.Type, "Selector")
			for _, proxyName := range group.All {
				proxyName := proxyName
				item := groupMenu.AddCheckbox(proxyName, proxyName == group.Now)
				if !selectable {
					item.SetEnabled(false)
					continue
				}
				item.OnClick(func(*application.Context) {
					go a.selectTrayProxy(group.Name, proxyName)
				})
			}
		}
	}

	menu.AddSeparator()
	menu.Add("退出 zashdesktop").OnClick(func(*application.Context) {
		a.quit()
	})
	a.tray.SetMenu(menu)
}

func (a *App) trayStartCore() {
	if a.coreService == nil {
		return
	}
	config, err := a.coreService.GetConfig()
	if err != nil {
		debugLogf("tray", "tray start core get config: %v", err)
		return
	}
	if !config.Installed {
		debugLogf("tray", "tray start core: core not installed")
		return
	}
	if _, err := a.coreService.StartCore(config.RunArgs, config.CoreType); err != nil {
		debugLogf("tray", "tray start core failed: %v", err)
	}
}

func (a *App) trayStopCore() {
	if a.coreService == nil {
		return
	}
	if _, err := a.coreService.StopCore(); err != nil {
		debugLogf("tray", "tray stop core failed: %v", err)
	}
}

func (a *App) trayRestartCore() {
	if a.coreService == nil {
		return
	}
	config, err := a.coreService.GetConfig()
	if err != nil {
		debugLogf("tray", "tray restart core get config: %v", err)
		return
	}
	if !config.Installed {
		debugLogf("tray", "tray restart core: core not installed")
		return
	}
	if _, err := a.coreService.RestartCore(config.RunArgs, config.CoreType); err != nil {
		debugLogf("tray", "tray restart core failed: %v", err)
	}
}

func (a *App) selectTrayProxy(group, proxy string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := selectClashProxy(ctx, a.trayAPIURL(), a.launch.APISecret, group, proxy); err != nil {
		debugLogf("tray", "select tray proxy: group=%q proxy=%q err=%v", group, proxy, err)
		return
	}
	a.refreshTrayProxyGroups(context.Background())
}

func (a *App) fetchTrayProxyGroupsCached(ctx context.Context) ([]trayProxy, string, error) {
	body, err := fetchTrayProxyRawBody(ctx, a.trayAPIURL(), a.launch.APISecret)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(body)
	hash := hex.EncodeToString(h[:])

	a.mu.Lock()
	if hash == a.trayProxyResponseHash && a.trayProxyCachedGroups != nil {
		cached := a.trayProxyCachedGroups
		a.mu.Unlock()
		return cached, hash, nil
	}
	a.mu.Unlock()

	groups, err := parseTrayProxyGroups(body)
	if err != nil {
		return nil, "", err
	}

	a.mu.Lock()
	a.trayProxyResponseHash = hash
	a.trayProxyCachedGroups = groups
	a.mu.Unlock()

	return groups, hash, nil
}

func fetchTrayProxyRawBody(ctx context.Context, apiURL, secret string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/proxies", nil)
	if err != nil {
		return nil, err
	}
	setTrayProxyAuthorization(request, secret)

	response, err := trayProxyHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Clash API returned %s", response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, maxTrayProxyResponse))
}

func parseTrayProxyGroups(data []byte) ([]trayProxy, error) {
	var payload trayProxyResponse
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode Clash proxies: %w", err)
	}

	groups := make([]trayProxy, 0, len(payload.Proxies))
	for name, proxy := range payload.Proxies {
		if proxy.Name == "" {
			proxy.Name = name
		}
		if proxy.Name == "GLOBAL" || len(proxy.All) == 0 {
			continue
		}
		if strings.EqualFold(proxy.Type, "Selector") || strings.EqualFold(proxy.Type, "URLTest") {
			groups = append(groups, proxy)
		}
	}

	global := payload.Proxies["GLOBAL"]
	order := make(map[string]int, len(global.All))
	for index, name := range global.All {
		order[name] = index
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, leftOK := order[groups[i].Name]
		right, rightOK := order[groups[j].Name]
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK {
			return left < right
		}
		return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name)
	})

	if len(global.All) > 0 {
		if global.Name == "" {
			global.Name = "GLOBAL"
		}
		groups = append(groups, global)
	}
	return groups, nil
}


func selectClashProxy(ctx context.Context, apiURL, secret, group, proxy string) error {
	body, err := json.Marshal(map[string]string{"name": proxy})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(apiURL, "/") + "/proxies/" + url.PathEscape(group)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	setTrayProxyAuthorization(request, secret)

	response, err := trayProxyHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("Clash API returned %s", response.Status)
	}
	return nil
}

func setTrayProxyAuthorization(request *http.Request, secret string) {
	if secret = strings.TrimSpace(secret); secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
}

func (a *App) trayAPIURL() string {
	if a.coreService != nil {
		a.coreService.mu.Lock()
		apiURL := a.coreService.trayAPIURL
		a.coreService.mu.Unlock()
		if apiURL != "" {
			return apiURL
		}
	}
	return defaultTrayAPIURL
}

func normalizeTrayAPIURL(rawURL string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if trimmed == "" {
		return defaultTrayAPIURL, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("请输入有效的托盘 API 地址")
	}
	return trimmed, nil
}
