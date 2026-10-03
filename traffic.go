package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ===================== 1. 数据模型 =====================

type TrafficItem struct {
	Name  string `json:"name"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Count int64  `json:"count"`
}

type TrafficRankRequest struct {
	Dimension string `json:"dimension"`
	PageNum   int    `json:"pageNum,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

type TrafficRankResult struct {
	List     []TrafficItem `json:"list"`
	Total    int           `json:"total"`
	PageNum  int           `json:"pageNum"`
	PageSize int           `json:"pageSize"`
}

type ClashConnection struct {
	ID       string        `json:"id"`
	Upload   int64         `json:"upload"`
	Download int64         `json:"download"`
	Chains   []string      `json:"chains"`
	Rule     string        `json:"rule"`
	Metadata ClashMetadata `json:"metadata"`
}

type ClashMetadata struct {
	Host          string `json:"host"`
	DestinationIP string `json:"destinationIP"`
	SourceIP      string `json:"sourceIP"`
	ProcessPath   string `json:"processPath"`
	Process       string `json:"process"`
	ProcessName   string `json:"processName"`
	PackageName   string `json:"packageName"`
}

type ClashConnectionsSnapshot struct {
	Connections []ClashConnection `json:"connections"`
}

// ===================== 2. 统计引擎与存储 =====================

const (
	maxDimensionItems = 1500
	trimDimensionKeep = 1000
)

type connMetaCache struct {
	clientID    string
	destination string
	process     string
	node        string
	rule        string
}

type lastConnTraffic struct {
	Download int64
	Upload   int64
	meta     connMetaCache
}

type trafficPersistData struct {
	Clients   map[string]*TrafficItem `json:"clients"`
	Domains   map[string]*TrafficItem `json:"domains"`
	Processes map[string]*TrafficItem `json:"processes"`
	Nodes     map[string]*TrafficItem `json:"nodes"`
	Rules     map[string]*TrafficItem `json:"rules"`
}

type TrafficStore struct {
	filePath        string
	mu              sync.RWMutex
	clients         map[string]*TrafficItem
	domains         map[string]*TrafficItem
	processes       map[string]*TrafficItem
	nodes           map[string]*TrafficItem
	rules           map[string]*TrafficItem
	lastConnections map[string]lastConnTraffic
	dirty           bool
	stopChan        chan struct{}
	wg              sync.WaitGroup
}

func NewTrafficStore(dataDir string) *TrafficStore {
	dir := filepath.Join(dataDir, "data")
	_ = os.MkdirAll(dir, 0755)

	store := &TrafficStore{
		filePath:        filepath.Join(dir, "traffic.json"),
		clients:         make(map[string]*TrafficItem),
		domains:         make(map[string]*TrafficItem),
		processes:       make(map[string]*TrafficItem),
		nodes:           make(map[string]*TrafficItem),
		rules:           make(map[string]*TrafficItem),
		lastConnections: make(map[string]lastConnTraffic),
		stopChan:        make(chan struct{}),
	}

	store.loadData()

	store.wg.Add(1)
	go store.flushLoop()
	return store
}

func (s *TrafficStore) loadData() {
	content, err := os.ReadFile(s.filePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			debugLogf("traffic", "failed to read traffic file %q: %v", s.filePath, err)
		}
		return
	}
	var data trafficPersistData
	if err := json.Unmarshal(content, &data); err != nil {
		debugLogf("traffic", "failed to parse traffic file %q: %v", s.filePath, err)
		return
	}
	if data.Clients != nil {
		s.clients = data.Clients
	}
	if data.Domains != nil {
		s.domains = data.Domains
	}
	if data.Processes != nil {
		s.processes = data.Processes
	}
	if data.Nodes != nil {
		s.nodes = data.Nodes
	}
	if data.Rules != nil {
		s.rules = data.Rules
	}
	debugLogf("traffic", "loaded traffic history: clients=%d, domains=%d, processes=%d, nodes=%d, rules=%d",
		len(s.clients), len(s.domains), len(s.processes), len(s.nodes), len(s.rules))
}

func (s *TrafficStore) Close() {
	close(s.stopChan)
	s.wg.Wait()
	s.Flush()
}

func (s *TrafficStore) flushLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.Flush()
		}
	}
}

func cloneTrafficMap(src map[string]*TrafficItem) map[string]*TrafficItem {
	dst := make(map[string]*TrafficItem, len(src))
	for k, v := range src {
		if v != nil {
			itemCopy := *v
			dst[k] = &itemCopy
		}
	}
	return dst
}

func (s *TrafficStore) trimMapIfNeededLocked(targetMap map[string]*TrafficItem) {
	if len(targetMap) <= maxDimensionItems {
		return
	}
	items := make([]*TrafficItem, 0, len(targetMap))
	for _, item := range targetMap {
		if item != nil {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return (items[i].Down + items[i].Up) > (items[j].Down + items[j].Up)
	})
	for i := trimDimensionKeep; i < len(items); i++ {
		delete(targetMap, items[i].Name)
	}
}

func (s *TrafficStore) Flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}

	// 1. 容量超限时淘汰低流量项，防止内存与磁盘文件无界增长
	s.trimMapIfNeededLocked(s.domains)
	s.trimMapIfNeededLocked(s.clients)
	s.trimMapIfNeededLocked(s.processes)
	s.trimMapIfNeededLocked(s.nodes)
	s.trimMapIfNeededLocked(s.rules)

	// 2. 浅克隆快照后立刻释放锁，避免序列化和磁盘 I/O 阻塞实时统计与前端查询
	data := trafficPersistData{
		Clients:   cloneTrafficMap(s.clients),
		Domains:   cloneTrafficMap(s.domains),
		Processes: cloneTrafficMap(s.processes),
		Nodes:     cloneTrafficMap(s.nodes),
		Rules:     cloneTrafficMap(s.rules),
	}
	s.dirty = false
	filePath := s.filePath
	s.mu.Unlock()

	// 3. 锁外执行序列化与落盘
	bytes, err := json.Marshal(data)
	if err != nil {
		debugLogf("traffic", "failed to marshal traffic data: %v", err)
		return
	}
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0644); err == nil {
		if err := os.Rename(tmp, filePath); err != nil {
			_ = os.WriteFile(filePath, bytes, 0644)
			_ = os.Remove(tmp)
		}
	} else {
		_ = os.WriteFile(filePath, bytes, 0644)
	}
	debugLogf("traffic", "saved traffic snapshot to disk (clients=%d, domains=%d)", len(data.Clients), len(data.Domains))
}

func getProcessName(m *ClashMetadata) string {
	if m == nil {
		return "system"
	}
	proc := m.ProcessPath
	if proc == "" {
		proc = m.Process
	}
	if proc == "" {
		proc = m.ProcessName
	}
	if proc == "" {
		proc = m.PackageName
	}
	if proc == "" || proc == "system" {
		return "system"
	}
	cleaned := filepath.Base(filepath.FromSlash(proc))
	if cleaned == "." || cleaned == "" {
		return "system"
	}
	return cleaned
}

func isLocalClientIP(ip string) bool {
	if ip == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(ip))
	if lower == "localhost" || lower == "::1" || strings.HasPrefix(lower, "127.") ||
		strings.HasPrefix(lower, "fdfe") || strings.HasPrefix(lower, "172.18.") {
		return true
	}
	parsed := net.ParseIP(lower)
	return parsed != nil && parsed.IsLoopback()
}

func getClientIdentity(m *ClashMetadata, process string) string {
	sourceIP := "unknown"
	if m != nil && m.SourceIP != "" {
		sourceIP = m.SourceIP
	}
	if isLocalClientIP(sourceIP) && process != "" && process != "system" {
		return process
	}
	return sourceIP
}

func (s *TrafficStore) HandleConnections(conns []ClashConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentIDs := make(map[string]struct{}, len(conns))
	changed := false

	for _, conn := range conns {
		id := conn.ID
		currentIDs[id] = struct{}{}
		prev, hadPrev := s.lastConnections[id]
		isNew := !hadPrev
		diffDown := conn.Download - prev.Download
		diffUp := conn.Upload - prev.Upload

		// 首次接入新连接，或已有连接产生新流量增量时才进行更新
		if isNew || diffDown > 0 || diffUp > 0 {
			var meta connMetaCache
			if hadPrev {
				// 长连接直接复用元数据，避免每秒重复调用 filepath.Base、strings.ToLower 与 net.ParseIP
				meta = prev.meta
			} else {
				process := getProcessName(&conn.Metadata)
				clientID := getClientIdentity(&conn.Metadata, process)
				destination := conn.Metadata.Host
				if destination == "" {
					destination = conn.Metadata.DestinationIP
				}
				if destination == "" {
					destination = "unknown"
				}
				node := "DIRECT"
				if len(conn.Chains) > 0 && conn.Chains[0] != "" {
					node = conn.Chains[0]
				}
				rule := conn.Rule
				if rule == "" {
					rule = "Match"
				}
				meta = connMetaCache{
					clientID:    clientID,
					destination: destination,
					process:     process,
					node:        node,
					rule:        rule,
				}
			}

			if diffDown < 0 {
				diffDown = 0
			}
			if diffUp < 0 {
				diffUp = 0
			}

			addTraffic(s.clients, meta.clientID, diffUp, diffDown, isNew)
			addTraffic(s.domains, meta.destination, diffUp, diffDown, isNew)
			addTraffic(s.processes, meta.process, diffUp, diffDown, isNew)
			addTraffic(s.nodes, meta.node, diffUp, diffDown, isNew)
			addTraffic(s.rules, meta.rule, diffUp, diffDown, isNew)
			changed = true
			s.lastConnections[id] = lastConnTraffic{
				Download: conn.Download,
				Upload:   conn.Upload,
				meta:     meta,
			}
		} else {
			s.lastConnections[id] = prev
		}
	}

	for id := range s.lastConnections {
		if _, exists := currentIDs[id]; !exists {
			delete(s.lastConnections, id)
		}
	}
	if changed {
		s.dirty = true
	}
}

func addTraffic(targetMap map[string]*TrafficItem, name string, up, down int64, isNew bool) {
	if name == "" {
		return
	}
	item, exists := targetMap[name]
	if !exists {
		item = &TrafficItem{Name: name}
		targetMap[name] = item
	}
	item.Up += up
	item.Down += down
	if isNew {
		item.Count++
	}
}

func (s *TrafficStore) GetRank(req TrafficRankRequest) TrafficRankResult {
	s.mu.RLock()

	res := TrafficRankResult{
		List:     make([]TrafficItem, 0),
		PageNum:  req.PageNum,
		PageSize: req.PageSize,
	}
	if res.PageNum <= 0 {
		res.PageNum = 1
	}
	if res.PageSize <= 0 {
		res.PageSize = 200
	}

	var targetMap map[string]*TrafficItem
	switch req.Dimension {
	case "clients":
		targetMap = s.clients
	case "domains":
		targetMap = s.domains
	case "processes":
		targetMap = s.processes
	case "nodes":
		targetMap = s.nodes
	case "rules":
		targetMap = s.rules
	default:
		s.mu.RUnlock()
		return res
	}

	items := make([]TrafficItem, 0, len(targetMap))
	for _, item := range targetMap {
		if item != nil {
			items = append(items, *item)
		}
	}
	s.mu.RUnlock() // 拷贝完后立即释放读锁！排序在锁外进行，避免长时间阻塞写入

	sort.Slice(items, func(i, j int) bool {
		return (items[i].Down + items[i].Up) > (items[j].Down + items[j].Up)
	})

	total := len(items)
	res.Total = total
	start := (res.PageNum - 1) * res.PageSize
	if start >= total {
		return res
	}
	end := start + res.PageSize
	if end > total {
		end = total
	}
	res.List = items[start:end]
	return res
}

func (s *TrafficStore) ClearData() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clients = make(map[string]*TrafficItem)
	s.domains = make(map[string]*TrafficItem)
	s.processes = make(map[string]*TrafficItem)
	s.nodes = make(map[string]*TrafficItem)
	s.rules = make(map[string]*TrafficItem)
	s.dirty = true
	_ = os.Remove(s.filePath)
	debugLogf("traffic", "traffic stats cleared")
	return nil
}

// ===================== 3. WebSocket 采集器 =====================

type TrafficCollector struct {
	store      *TrafficStore
	mu         sync.Mutex
	currentURL string
	secret     string
	cancelFunc context.CancelFunc
	running    bool
}

func NewTrafficCollector(store *TrafficStore) *TrafficCollector {
	return &TrafficCollector{store: store}
}

func (c *TrafficCollector) UpdateTarget(apiURL, secret string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	secret = strings.TrimSpace(secret)
	if c.currentURL == apiURL && c.secret == secret && c.running {
		return
	}
	if c.cancelFunc != nil {
		c.cancelFunc()
		c.cancelFunc = nil
		c.running = false
	}
	c.currentURL = apiURL
	c.secret = secret
	if apiURL == "" {
		debugLogf("traffic", "collector target cleared, stopping collection")
		c.store.Flush()
		return
	}
	debugLogf("traffic", "collector target updated: url=%s, hasSecret=%t", apiURL, secret != "")
	ctx, cancel := context.WithCancel(context.Background())
	c.cancelFunc = cancel
	c.running = true
	go c.runConnections(ctx, apiURL, secret)
}

func (c *TrafficCollector) Stop() {
	c.mu.Lock()
	if c.cancelFunc != nil {
		c.cancelFunc()
		c.cancelFunc = nil
	}
	c.running = false
	c.mu.Unlock()
	debugLogf("traffic", "collector stopped")
	c.store.Flush()
}

func (c *TrafficCollector) runConnections(ctx context.Context, apiURL, secret string) {
	wsBase := apiURL
	if strings.HasPrefix(wsBase, "http://") {
		wsBase = "ws://" + strings.TrimPrefix(wsBase, "http://")
	} else if strings.HasPrefix(wsBase, "https://") {
		wsBase = "wss://" + strings.TrimPrefix(wsBase, "https://")
	} else if !strings.HasPrefix(wsBase, "ws://") && !strings.HasPrefix(wsBase, "wss://") {
		wsBase = "ws://" + wsBase
	}
	wsURL := fmt.Sprintf("%s/connections", strings.TrimRight(wsBase, "/"))

	opts := &websocket.DialOptions{HTTPHeader: make(http.Header)}
	if secret != "" {
		opts.HTTPHeader.Set("Authorization", "Bearer "+secret)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, _, err := websocket.Dial(ctx, wsURL, opts)
		if err != nil {
			debugLogf("traffic", "dial /connections failed (%s): %v, will retry in 2s", wsURL, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		debugLogf("traffic", "connected to /connections stream: %s", wsURL)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				debugLogf("traffic", "connections stream closed: %v, will reconnect in 1s", err)
				break
			}
			var snapshot ClashConnectionsSnapshot
			if err := json.Unmarshal(data, &snapshot); err == nil && len(snapshot.Connections) > 0 {
				c.store.HandleConnections(snapshot.Connections)
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// ===================== 4. CoreService 导出方法 =====================

func (s *CoreService) GetTrafficRank(req TrafficRankRequest) (TrafficRankResult, error) {
	if s.trafficStore == nil {
		debugLogf("traffic", "GetTrafficRank failed: traffic store not initialized")
		return TrafficRankResult{}, errors.New("traffic store not initialized")
	}
	return s.trafficStore.GetRank(req), nil
}

func (s *CoreService) ClearTrafficData() error {
	if s.trafficStore == nil {
		debugLogf("traffic", "ClearTrafficData failed: traffic store not initialized")
		return errors.New("traffic store not initialized")
	}
	return s.trafficStore.ClearData()
}
