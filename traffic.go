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
	TimeRange string `json:"timeRange,omitempty"`
	OrderBy   string `json:"orderBy,omitempty"`
	OrderDesc bool   `json:"orderDesc,omitempty"`
	PageNum   int    `json:"pageNum,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

type TrafficRankResult struct {
	List      []TrafficItem `json:"list"`
	Total     int           `json:"total"`
	PageNum   int           `json:"pageNum"`
	PageSize  int           `json:"pageSize"`
	StartTime int64         `json:"startTime"`
}

type TrafficMeta struct {
	StartTime         int64  `json:"startTime"`
	AutoCleanInterval string `json:"autoCleanInterval"`
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
	maxTotalDimensionItems  = 1500
	trimTotalDimensionKeep  = 1000
	maxBucketDimensionItems = 400
	trimBucketDimensionKeep = 250
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

type DimensionData struct {
	Clients   map[string]*TrafficItem `json:"clients,omitempty"`
	Domains   map[string]*TrafficItem `json:"domains,omitempty"`
	Processes map[string]*TrafficItem `json:"processes,omitempty"`
	Nodes     map[string]*TrafficItem `json:"nodes,omitempty"`
	Rules     map[string]*TrafficItem `json:"rules,omitempty"`
}

func newDimensionData() *DimensionData {
	return &DimensionData{
		Clients:   make(map[string]*TrafficItem),
		Domains:   make(map[string]*TrafficItem),
		Processes: make(map[string]*TrafficItem),
		Nodes:     make(map[string]*TrafficItem),
		Rules:     make(map[string]*TrafficItem),
	}
}

func (d *DimensionData) getMap(dimension string) map[string]*TrafficItem {
	if d == nil {
		return nil
	}
	switch dimension {
	case "clients":
		return d.Clients
	case "domains":
		return d.Domains
	case "processes":
		return d.Processes
	case "nodes":
		return d.Nodes
	case "rules":
		return d.Rules
	default:
		return nil
	}
}

func (d *DimensionData) add(meta *connMetaCache, up, down int64, isNew bool) {
	if d == nil {
		return
	}
	if d.Clients == nil {
		d.Clients = make(map[string]*TrafficItem)
	}
	if d.Domains == nil {
		d.Domains = make(map[string]*TrafficItem)
	}
	if d.Processes == nil {
		d.Processes = make(map[string]*TrafficItem)
	}
	if d.Nodes == nil {
		d.Nodes = make(map[string]*TrafficItem)
	}
	if d.Rules == nil {
		d.Rules = make(map[string]*TrafficItem)
	}

	addTraffic(d.Clients, meta.clientID, up, down, isNew)
	addTraffic(d.Domains, meta.destination, up, down, isNew)
	addTraffic(d.Processes, meta.process, up, down, isNew)
	addTraffic(d.Nodes, meta.node, up, down, isNew)
	addTraffic(d.Rules, meta.rule, up, down, isNew)
}

func (d *DimensionData) clone() *DimensionData {
	if d == nil {
		return nil
	}
	return &DimensionData{
		Clients:   cloneTrafficMap(d.Clients),
		Domains:   cloneTrafficMap(d.Domains),
		Processes: cloneTrafficMap(d.Processes),
		Nodes:     cloneTrafficMap(d.Nodes),
		Rules:     cloneTrafficMap(d.Rules),
	}
}

type trafficPersistData struct {
	// 兼容老版本顶层字段
	LegacyClients   map[string]*TrafficItem `json:"clients,omitempty"`
	LegacyDomains   map[string]*TrafficItem `json:"domains,omitempty"`
	LegacyProcesses map[string]*TrafficItem `json:"processes,omitempty"`
	LegacyNodes     map[string]*TrafficItem `json:"nodes,omitempty"`
	LegacyRules     map[string]*TrafficItem `json:"rules,omitempty"`

	// 新版本分层分桶字段
	Total  *DimensionData            `json:"total,omitempty"`
	Hourly map[string]*DimensionData `json:"hourly,omitempty"` // key: 2006010215
	Daily  map[string]*DimensionData `json:"daily,omitempty"`  // key: 20060102

	// 统计起始时间与自动清理周期
	StartTime         int64  `json:"startTime,omitempty"`
	AutoCleanInterval string `json:"autoCleanInterval,omitempty"`
}

type TrafficStore struct {
	filePath          string
	mu                sync.RWMutex
	total             *DimensionData
	hourly            map[string]*DimensionData
	daily             map[string]*DimensionData
	lastConnections   map[string]lastConnTraffic
	startTime         int64
	autoCleanInterval string
	dirty             bool
	stopChan          chan struct{}
	wg                sync.WaitGroup
}

func NewTrafficStore(dataDir string) *TrafficStore {
	dir := filepath.Join(dataDir, "data")
	_ = os.MkdirAll(dir, 0755)

	store := &TrafficStore{
		filePath:          filepath.Join(dir, "traffic.json"),
		total:             newDimensionData(),
		hourly:            make(map[string]*DimensionData),
		daily:             make(map[string]*DimensionData),
		lastConnections:   make(map[string]lastConnTraffic),
		startTime:         time.Now().UnixMilli(),
		autoCleanInterval: "month",
		stopChan:          make(chan struct{}),
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

	if data.Total != nil {
		s.total = data.Total
	} else if data.LegacyClients != nil || data.LegacyDomains != nil {
		s.total = &DimensionData{
			Clients:   data.LegacyClients,
			Domains:   data.LegacyDomains,
			Processes: data.LegacyProcesses,
			Nodes:     data.LegacyNodes,
			Rules:     data.LegacyRules,
		}
	}
	if s.total == nil {
		s.total = newDimensionData()
	}
	if s.total.Clients == nil {
		s.total.Clients = make(map[string]*TrafficItem)
	}
	if s.total.Domains == nil {
		s.total.Domains = make(map[string]*TrafficItem)
	}
	if s.total.Processes == nil {
		s.total.Processes = make(map[string]*TrafficItem)
	}
	if s.total.Nodes == nil {
		s.total.Nodes = make(map[string]*TrafficItem)
	}
	if s.total.Rules == nil {
		s.total.Rules = make(map[string]*TrafficItem)
	}

	if data.Hourly != nil {
		s.hourly = data.Hourly
	}
	if data.Daily != nil {
		s.daily = data.Daily
	}

	if data.StartTime > 0 {
		s.startTime = data.StartTime
	} else {
		s.startTime = time.Now().UnixMilli()
	}
	if data.AutoCleanInterval != "" {
		s.autoCleanInterval = data.AutoCleanInterval
	} else {
		s.autoCleanInterval = "month"
	}

	debugLogf("traffic", "loaded traffic history: total_domains=%d, hourly_buckets=%d, daily_buckets=%d, autoClean=%s",
		len(s.total.Domains), len(s.hourly), len(s.daily), s.autoCleanInterval)
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

func getAutoCleanDuration(interval string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "week":
		return 7 * 24 * time.Hour
	case "month":
		return 30 * 24 * time.Hour
	case "quarter":
		return 90 * 24 * time.Hour
	default:
		return 0
	}
}

func (s *TrafficStore) clearDataLocked() {
	s.total = newDimensionData()
	s.hourly = make(map[string]*DimensionData)
	s.daily = make(map[string]*DimensionData)
	s.lastConnections = make(map[string]lastConnTraffic)
}

func (s *TrafficStore) checkAutoCleanLocked(now time.Time) {
	dur := getAutoCleanDuration(s.autoCleanInterval)
	if dur <= 0 || s.startTime <= 0 {
		return
	}
	if now.Sub(time.UnixMilli(s.startTime)) >= dur {
		s.clearDataLocked()
		s.startTime = now.UnixMilli()
		s.dirty = true
		debugLogf("traffic", "auto cleaned traffic history in background (interval: %s, new startTime: %d)",
			s.autoCleanInterval, s.startTime)
	}
}

func cloneTrafficMap(src map[string]*TrafficItem) map[string]*TrafficItem {
	if src == nil {
		return nil
	}
	dst := make(map[string]*TrafficItem, len(src))
	for k, v := range src {
		if v != nil {
			itemCopy := *v
			dst[k] = &itemCopy
		}
	}
	return dst
}

func trimMapIfNeeded(targetMap *map[string]*TrafficItem, maxLimit, keepLimit int) {
	if targetMap == nil || *targetMap == nil {
		return
	}
	m := *targetMap
	if len(m) <= maxLimit {
		return
	}
	items := make([]*TrafficItem, 0, len(m))
	for _, item := range m {
		if item != nil {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return (items[i].Down + items[i].Up) > (items[j].Down + items[j].Up)
	})
	newMap := make(map[string]*TrafficItem, keepLimit)
	for i := 0; i < keepLimit && i < len(items); i++ {
		newMap[items[i].Name] = items[i]
	}
	*targetMap = newMap
}

func trimDimensionData(d *DimensionData, maxLimit, keepLimit int) {
	if d == nil {
		return
	}
	trimMapIfNeeded(&d.Domains, maxLimit, keepLimit)
	trimMapIfNeeded(&d.Clients, maxLimit, keepLimit)
	trimMapIfNeeded(&d.Processes, maxLimit, keepLimit)
	trimMapIfNeeded(&d.Nodes, maxLimit, keepLimit)
	trimMapIfNeeded(&d.Rules, maxLimit, keepLimit)
}

func (s *TrafficStore) Flush() {
	s.mu.Lock()
	now := time.Now()

	// 0. 后台无人值守自动清理：检查是否达到预设的自动清理周期
	s.checkAutoCleanLocked(now)

	if !s.dirty {
		s.mu.Unlock()
		return
	}

	hourKey := now.Format("2006010215")
	dayKey := now.Format("20060102")

	// 1. 清理超过 24 小时前的小时桶（最多保留 24 个）
	hourCutoff := now.Add(-24 * time.Hour).Format("2006010215")
	for k := range s.hourly {
		if k < hourCutoff {
			delete(s.hourly, k)
		}
	}

	// 2. 清理超过 30 天前的天桶（最多保留 30 个）
	dayCutoff := now.AddDate(0, 0, -30).Format("20060102")
	for k := range s.daily {
		if k < dayCutoff {
			delete(s.daily, k)
		}
	}

	// 3. 维度容量截断：历史桶早已截断过且不再增长，仅对正在累积的活跃桶截断，大幅降低排序开销
	trimDimensionData(s.total, maxTotalDimensionItems, trimTotalDimensionKeep)
	if hBucket := s.hourly[hourKey]; hBucket != nil {
		trimDimensionData(hBucket, maxBucketDimensionItems, trimBucketDimensionKeep)
	}
	if dBucket := s.daily[dayKey]; dBucket != nil {
		trimDimensionData(dBucket, maxBucketDimensionItems, trimBucketDimensionKeep)
	}

	// 4. 制作落盘快照：仅克隆正在写入的活跃桶，历史不可变桶浅引用，立即释放写锁！
	hourlyClone := make(map[string]*DimensionData, len(s.hourly))
	for k, v := range s.hourly {
		if v != nil {
			if k == hourKey {
				hourlyClone[k] = v.clone()
			} else {
				hourlyClone[k] = v
			}
		}
	}
	dailyClone := make(map[string]*DimensionData, len(s.daily))
	for k, v := range s.daily {
		if v != nil {
			if k == dayKey {
				dailyClone[k] = v.clone()
			} else {
				dailyClone[k] = v
			}
		}
	}
	data := trafficPersistData{
		Total:             s.total.clone(),
		Hourly:            hourlyClone,
		Daily:             dailyClone,
		StartTime:         s.startTime,
		AutoCleanInterval: s.autoCleanInterval,
	}
	s.dirty = false
	filePath := s.filePath
	s.mu.Unlock() // 极速释放排他写锁，避免阻塞 WebSocket 统计与前端读取

	// 5. 锁外执行序列化与原子落盘
	bytes, err := json.Marshal(data)
	if err != nil {
		debugLogf("traffic", "failed to marshal traffic data: %v", err)
		return
	}
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0644); err != nil {
		debugLogf("traffic", "failed to write traffic tmp file %q: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, filePath); err != nil {
		debugLogf("traffic", "failed to rename %q to %q: %v, attempting overwrite fallback", tmp, filePath, err)
		if writeErr := os.WriteFile(filePath, bytes, 0644); writeErr != nil {
			debugLogf("traffic", "failed fallback write to %q: %v", filePath, writeErr)
		}
		_ = os.Remove(tmp)
	}
	debugLogf("traffic", "saved traffic snapshot to disk (total_domains=%d, hourly=%d, daily=%d)",
		len(data.Total.Domains), len(data.Hourly), len(data.Daily))
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

	now := time.Now()
	hourKey := now.Format("2006010215")
	dayKey := now.Format("20060102")

	hBucket, hExists := s.hourly[hourKey]
	if !hExists {
		hBucket = newDimensionData()
		s.hourly[hourKey] = hBucket
	}

	dBucket, dExists := s.daily[dayKey]
	if !dExists {
		dBucket = newDimensionData()
		s.daily[dayKey] = dBucket
	}

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

			// 同时增量记账到：全量总桶、当前小时桶、当天桶
			s.total.add(&meta, diffUp, diffDown, isNew)
			hBucket.add(&meta, diffUp, diffDown, isNew)
			dBucket.add(&meta, diffUp, diffDown, isNew)

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
		item = &TrafficItem{Name: name, Count: 1}
		targetMap[name] = item
	} else if isNew {
		item.Count++
	}
	item.Up += up
	item.Down += down
}

func addTrafficItemToMerged(target map[string]*TrafficItem, item *TrafficItem) {
	if item == nil || item.Name == "" {
		return
	}
	exist, ok := target[item.Name]
	if !ok {
		exist = &TrafficItem{Name: item.Name}
		target[item.Name] = exist
	}
	exist.Up += item.Up
	exist.Down += item.Down
	exist.Count += item.Count
}

func formatRankResult(res TrafficRankResult, items []TrafficItem, req TrafficRankRequest) TrafficRankResult {
	orderDesc := true
	if req.OrderBy != "" {
		orderDesc = req.OrderDesc
	}

	orderBy := strings.ToLower(strings.TrimSpace(req.OrderBy))
	sort.Slice(items, func(i, j int) bool {
		var a, b int64
		switch orderBy {
		case "download":
			a, b = items[i].Down, items[j].Down
		case "upload":
			a, b = items[i].Up, items[j].Up
		case "count":
			a, b = items[i].Count, items[j].Count
		case "total":
			fallthrough
		default:
			a, b = items[i].Down+items[i].Up, items[j].Down+items[j].Up
		}
		if a == b {
			return (items[i].Down + items[i].Up) > (items[j].Down + items[j].Up)
		}
		if orderDesc {
			return a > b
		}
		return a < b
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

func (s *TrafficStore) GetRank(req TrafficRankRequest) TrafficRankResult {
	s.mu.RLock()

	res := TrafficRankResult{
		List:      make([]TrafficItem, 0),
		PageNum:   req.PageNum,
		PageSize:  req.PageSize,
		StartTime: s.startTime,
	}
	if res.PageNum <= 0 {
		res.PageNum = 1
	}
	if res.PageSize <= 0 {
		res.PageSize = 200
	}

	dimension := req.Dimension
	timeRange := strings.ToLower(strings.TrimSpace(req.TimeRange))
	if timeRange == "" {
		timeRange = "all"
	}

	now := time.Now()
	var mapsToMerge []map[string]*TrafficItem

	switch timeRange {
	case "24h":
		hourCutoff := now.Add(-24 * time.Hour).Format("2006010215")
		currentHourKey := now.Format("2006010215")
		for k, bucket := range s.hourly {
			if k >= hourCutoff && bucket != nil {
				m := bucket.getMap(dimension)
				if len(m) > 0 {
					if k == currentHourKey {
						mapsToMerge = append(mapsToMerge, cloneTrafficMap(m))
					} else {
						mapsToMerge = append(mapsToMerge, m)
					}
				}
			}
		}
		s.mu.RUnlock()

	case "7d":
		dayCutoff := now.AddDate(0, 0, -7).Format("20060102")
		currentDayKey := now.Format("20060102")
		for k, bucket := range s.daily {
			if k >= dayCutoff && bucket != nil {
				m := bucket.getMap(dimension)
				if len(m) > 0 {
					if k == currentDayKey {
						mapsToMerge = append(mapsToMerge, cloneTrafficMap(m))
					} else {
						mapsToMerge = append(mapsToMerge, m)
					}
				}
			}
		}
		s.mu.RUnlock()

	case "30d":
		dayCutoff := now.AddDate(0, 0, -30).Format("20060102")
		currentDayKey := now.Format("20060102")
		for k, bucket := range s.daily {
			if k >= dayCutoff && bucket != nil {
				m := bucket.getMap(dimension)
				if len(m) > 0 {
					if k == currentDayKey {
						mapsToMerge = append(mapsToMerge, cloneTrafficMap(m))
					} else {
						mapsToMerge = append(mapsToMerge, m)
					}
				}
			}
		}
		s.mu.RUnlock()

	default: // "all"
		targetMap := s.total.getMap(dimension)
		if targetMap == nil {
			s.mu.RUnlock()
			return res
		}
		items := make([]TrafficItem, 0, len(targetMap))
		for _, item := range targetMap {
			if item != nil {
				items = append(items, *item)
			}
		}
		s.mu.RUnlock()
		return formatRankResult(res, items, req)
	}

	merged := make(map[string]*TrafficItem)
	for _, m := range mapsToMerge {
		for _, item := range m {
			addTrafficItemToMerged(merged, item)
		}
	}
	items := make([]TrafficItem, 0, len(merged))
	for _, item := range merged {
		if item != nil {
			items = append(items, *item)
		}
	}
	return formatRankResult(res, items, req)
}

func (s *TrafficStore) ClearData() error {
	s.mu.Lock()
	s.clearDataLocked()
	s.startTime = time.Now().UnixMilli()
	s.dirty = true
	s.mu.Unlock()

	s.Flush()
	debugLogf("traffic", "traffic stats cleared and flushed")
	return nil
}

func (s *TrafficStore) GetMeta() TrafficMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return TrafficMeta{
		StartTime:         s.startTime,
		AutoCleanInterval: s.autoCleanInterval,
	}
}

func (s *TrafficStore) SetAutoCleanInterval(interval string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	interval = strings.ToLower(strings.TrimSpace(interval))
	switch interval {
	case "never", "week", "month", "quarter":
		s.autoCleanInterval = interval
	default:
		s.autoCleanInterval = "never"
	}
	s.dirty = true
	debugLogf("traffic", "auto clean interval updated to: %s", s.autoCleanInterval)
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

func (s *CoreService) GetTrafficMeta() (TrafficMeta, error) {
	if s.trafficStore == nil {
		debugLogf("traffic", "GetTrafficMeta failed: traffic store not initialized")
		return TrafficMeta{}, errors.New("traffic store not initialized")
	}
	return s.trafficStore.GetMeta(), nil
}

func (s *CoreService) SetTrafficAutoClean(interval string) error {
	if s.trafficStore == nil {
		debugLogf("traffic", "SetTrafficAutoClean failed: traffic store not initialized")
		return errors.New("traffic store not initialized")
	}
	return s.trafficStore.SetAutoCleanInterval(interval)
}
