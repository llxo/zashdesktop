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
	List          []TrafficItem `json:"list"`
	Total         int           `json:"total"`
	PageNum       int           `json:"pageNum"`
	PageSize      int           `json:"pageSize"`
	StartTime     int64         `json:"startTime"`
	TotalDownload int64         `json:"totalDownload"`
	TotalUpload   int64         `json:"totalUpload"`
	TotalCount    int64         `json:"totalCount"`
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
	maxTotalDimensionItems  = 800
	trimTotalDimensionKeep  = 500
	maxBucketDimensionItems = 500
	trimBucketDimensionKeep = 350
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
	TotalDownload int64                   `json:"totalDownload,omitempty"`
	TotalUpload   int64                   `json:"totalUpload,omitempty"`
	TotalCount    int64                   `json:"totalCount,omitempty"`
	Clients       map[string]*TrafficItem `json:"clients,omitempty"`
	Domains       map[string]*TrafficItem `json:"domains,omitempty"`
	Processes     map[string]*TrafficItem `json:"processes,omitempty"`
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

	d.TotalDownload += down
	d.TotalUpload += up
	if isNew {
		d.TotalCount++
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
		TotalDownload: d.TotalDownload,
		TotalUpload:   d.TotalUpload,
		TotalCount:    d.TotalCount,
		Clients:       cloneTrafficMap(d.Clients),
		Domains:       cloneTrafficMap(d.Domains),
		Processes:     cloneTrafficMap(d.Processes),
		Nodes:         cloneTrafficMap(d.Nodes),
		Rules:         cloneTrafficMap(d.Rules),
	}
}

type lastConnTrafficPersist struct {
	Download    int64  `json:"download"`
	Upload      int64  `json:"upload"`
	ClientID    string `json:"clientID"`
	Destination string `json:"destination"`
	Process     string `json:"process"`
	Node        string `json:"node"`
	Rule        string `json:"rule"`
}

type currentTrafficPersistData struct {
	Total             *DimensionData                    `json:"total,omitempty"`
	Today             *DimensionData                    `json:"today,omitempty"`
	TodayDate         string                            `json:"todayDate,omitempty"`
	LastConnections   map[string]lastConnTrafficPersist `json:"lastConnections,omitempty"`
	StartTime         int64                             `json:"startTime,omitempty"`
	AutoCleanInterval string                            `json:"autoCleanInterval,omitempty"`
}

type cachedRankResult struct {
	result    TrafficRankResult
	expiresAt time.Time
}

type TrafficStore struct {
	baseDir           string
	currentFile       string
	dailyDir          string
	mu                sync.RWMutex
	total             *DimensionData
	today             *DimensionData
	todayDate         string // 格式: 20060102
	lastConnections   map[string]lastConnTraffic
	startTime         int64
	autoCleanInterval string
	dirty             bool

	// 历史日桶不可变内存缓存（key: YYYYMMDD，最多缓存 30 天，占用约 1~2MB 内存，彻底消除 7d/30d 重复读盘与 JSON 反序列化）
	dailyCacheMu sync.RWMutex
	dailyCache   map[string]*DimensionData

	// 7d/30d 查询结果短期缓存（TTL ~2.5s，仅缓存最终的 Top 200 切片结果，零原始大对象常驻）
	queryCacheMu sync.Mutex
	queryCache   map[string]cachedRankResult

	stopChan chan struct{}
	wg       sync.WaitGroup
}

func NewTrafficStore(dataDir string) *TrafficStore {
	baseDir := filepath.Join(dataDir, "data", "traffic")
	dailyDir := filepath.Join(baseDir, "daily")
	_ = os.MkdirAll(dailyDir, 0755)

	store := &TrafficStore{
		baseDir:           baseDir,
		currentFile:       filepath.Join(baseDir, "current.json"),
		dailyDir:          dailyDir,
		total:             newDimensionData(),
		today:             newDimensionData(),
		todayDate:         time.Now().Format("20060102"),
		lastConnections:   make(map[string]lastConnTraffic),
		dailyCache:        make(map[string]*DimensionData),
		queryCache:        make(map[string]cachedRankResult),
		startTime:         time.Now().UnixMilli(),
		autoCleanInterval: "month",
		stopChan:          make(chan struct{}),
	}

	store.loadData()

	store.wg.Add(1)
	go store.flushLoop()
	return store
}

func atomicWriteJSON(filePath string, v any) error {
	bytes, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0644); err != nil {
		return fmt.Errorf("write tmp %q failed: %w", tmp, err)
	}
	if err := os.Rename(tmp, filePath); err != nil {
		debugLogf("traffic", "failed to rename %q to %q: %v, attempting overwrite fallback", tmp, filePath, err)
		if writeErr := os.WriteFile(filePath, bytes, 0644); writeErr != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("fallback write to %q failed: %w", filePath, writeErr)
		}
		_ = os.Remove(tmp)
	}
	return nil
}

func (s *TrafficStore) saveDailyFile(dateKey string, d *DimensionData) {
	if d == nil || dateKey == "" {
		return
	}
	// 同步写入内存缓存，克隆一份避免外部引用被后续变更
	s.dailyCacheMu.Lock()
	s.dailyCache[dateKey] = d.clone()
	s.dailyCacheMu.Unlock()

	filePath := filepath.Join(s.dailyDir, dateKey+".json")
	if err := atomicWriteJSON(filePath, d); err != nil {
		debugLogf("traffic", "failed to save daily traffic %s: %v", dateKey, err)
		return
	}
	debugLogf("traffic", "archived daily traffic file: %s", filePath)
}

func (s *TrafficStore) cleanOldDailyFiles(now time.Time) {
	cutoffDate := now.AddDate(0, 0, -30).Format("20060102")

	// 同步清理内存缓存中超过 30 天的历史天
	s.dailyCacheMu.Lock()
	for k := range s.dailyCache {
		if k < cutoffDate {
			delete(s.dailyCache, k)
		}
	}
	s.dailyCacheMu.Unlock()

	entries, err := os.ReadDir(s.dailyDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			baseName := strings.TrimSuffix(strings.TrimSuffix(name, ".tmp"), ".json")
			if len(baseName) == 8 && baseName < cutoffDate {
				_ = os.Remove(filepath.Join(s.dailyDir, name))
				debugLogf("traffic", "cleaned expired daily file: %s", name)
			}
		}
	}
}

// readDailyBucket 读取并反序列化单个历史天桶。优先命中不可变内存缓存，未命中时回退读取磁盘并写入缓存。
func (s *TrafficStore) readDailyBucket(dateKey string) *DimensionData {
	if dateKey == "" {
		return nil
	}

	s.dailyCacheMu.RLock()
	cached, ok := s.dailyCache[dateKey]
	s.dailyCacheMu.RUnlock()
	if ok {
		return cached
	}

	filePath := filepath.Join(s.dailyDir, dateKey+".json")
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	var bucket DimensionData
	if err := json.Unmarshal(content, &bucket); err != nil {
		debugLogf("traffic", "failed to parse daily traffic file %s: %v", filePath, err)
		return nil
	}

	s.dailyCacheMu.Lock()
	s.dailyCache[dateKey] = &bucket
	s.dailyCacheMu.Unlock()

	return &bucket
}

func (s *TrafficStore) loadData() {
	now := time.Now()
	todayKey := now.Format("20060102")

	content, err := os.ReadFile(s.currentFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			debugLogf("traffic", "failed to read current traffic file %q: %v", s.currentFile, err)
		}
		s.todayDate = todayKey
		return
	}
	var data currentTrafficPersistData
	if err := json.Unmarshal(content, &data); err != nil {
		debugLogf("traffic", "failed to parse current traffic file %q: %v", s.currentFile, err)
		s.todayDate = todayKey
		return
	}

	if data.Total != nil {
		s.total = data.Total
	}
	if s.total == nil {
		s.total = newDimensionData()
	}
	if data.StartTime > 0 {
		s.startTime = data.StartTime
	}
	if data.AutoCleanInterval != "" {
		s.autoCleanInterval = data.AutoCleanInterval
	}

	if data.LastConnections != nil {
		s.lastConnections = make(map[string]lastConnTraffic, len(data.LastConnections))
		for k, v := range data.LastConnections {
			s.lastConnections[k] = lastConnTraffic{
				Download: v.Download,
				Upload:   v.Upload,
				meta: connMetaCache{
					clientID:    v.ClientID,
					destination: v.Destination,
					process:     v.Process,
					node:        v.Node,
					rule:        v.Rule,
				},
			}
		}
	}

	// 检查关机/重启期间是否跨天
	if data.TodayDate != "" && data.TodayDate != todayKey {
		if data.Today != nil {
			trimDimensionData(data.Today, maxBucketDimensionItems, trimBucketDimensionKeep)
			s.saveDailyFile(data.TodayDate, data.Today)
		}
		s.today = newDimensionData()
		s.todayDate = todayKey
		s.dirty = true
	} else {
		if data.Today != nil {
			s.today = data.Today
		}
		s.todayDate = todayKey
	}

	debugLogf("traffic", "loaded traffic store: total_domains=%d, today_domains=%d, date=%s",
		len(s.total.Domains), len(s.today.Domains), s.todayDate)
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
	s.today = newDimensionData()
	s.todayDate = time.Now().Format("20060102")
	// 不能清空 s.lastConnections。避免长连接将其累计的全部历史流量倒灌进新统计中。
	// s.lastConnections = make(map[string]lastConnTraffic)

	s.queryCacheMu.Lock()
	s.queryCache = make(map[string]cachedRankResult)
	s.queryCacheMu.Unlock()

	s.dailyCacheMu.Lock()
	s.dailyCache = make(map[string]*DimensionData)
	s.dailyCacheMu.Unlock()

	entries, err := os.ReadDir(s.dailyDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && (strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".tmp")) {
				_ = os.Remove(filepath.Join(s.dailyDir, e.Name()))
			}
		}
	}
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

	newMap := make(map[string]*TrafficItem, keepLimit)

	// 1. 主轨保护：优先保留总流量（Down + Up）最高的条目（配额 75%）
	trafficKeep := (keepLimit * 3) / 4
	sort.Slice(items, func(i, j int) bool {
		return (items[i].Down + items[i].Up) > (items[j].Down + items[j].Up)
	})
	for i := 0; i < trafficKeep && i < len(items); i++ {
		newMap[items[i].Name] = items[i]
	}

	// 2. 双轨保护：保护高频连接条目（按 Count 排序补充，配额 15%）
	countLimit := trafficKeep + (keepLimit*15)/100
	sort.Slice(items, func(i, j int) bool {
		return items[i].Count > items[j].Count
	})
	for i := 0; i < len(items) && len(newMap) < countLimit; i++ {
		if _, exists := newMap[items[i].Name]; !exists {
			newMap[items[i].Name] = items[i]
		}
	}

	// 3. 补充保护：按上传量（Up）排序补充剩余配额，保护高上传单向流
	sort.Slice(items, func(i, j int) bool {
		return items[i].Up > items[j].Up
	})
	for i := 0; i < len(items) && len(newMap) < keepLimit; i++ {
		if _, exists := newMap[items[i].Name]; !exists {
			newMap[items[i].Name] = items[i]
		}
	}

	// 4. 兜底保护：若上述特定维度去重后仍未填满 keepLimit，按总流量顺序补充完毕
	if len(newMap) < keepLimit {
		sort.Slice(items, func(i, j int) bool {
			return (items[i].Down + items[i].Up) > (items[j].Down + items[j].Up)
		})
		for i := 0; i < len(items) && len(newMap) < keepLimit; i++ {
			if _, exists := newMap[items[i].Name]; !exists {
				newMap[items[i].Name] = items[i]
			}
		}
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

func (s *TrafficStore) rotateDayLocked(now time.Time, newTodayKey string) {
	oldDate := s.todayDate
	oldToday := s.today

	if oldDate != "" && oldToday != nil {
		trimDimensionData(oldToday, maxBucketDimensionItems, trimBucketDimensionKeep)
		s.saveDailyFile(oldDate, oldToday)
	}

	s.todayDate = newTodayKey
	s.today = newDimensionData()
	s.dirty = true

	s.cleanOldDailyFiles(now)

	s.queryCacheMu.Lock()
	s.queryCache = make(map[string]cachedRankResult)
	s.queryCacheMu.Unlock()
}

func (s *TrafficStore) checkDayRotationLocked(now time.Time) {
	todayKey := now.Format("20060102")
	if s.todayDate != todayKey {
		s.rotateDayLocked(now, todayKey)
	}
}

func (s *TrafficStore) checkDayRotation(now time.Time) {
	todayKey := now.Format("20060102")
	s.mu.RLock()
	needRotate := s.todayDate != todayKey
	s.mu.RUnlock()

	if needRotate {
		s.mu.Lock()
		if s.todayDate != todayKey {
			s.rotateDayLocked(now, todayKey)
		}
		s.mu.Unlock()
	}
}

func (s *TrafficStore) Flush() {
	s.mu.Lock()
	now := time.Now()

	s.checkAutoCleanLocked(now)
	s.checkDayRotationLocked(now)

	if !s.dirty {
		s.mu.Unlock()
		return
	}

	trimDimensionData(s.total, maxTotalDimensionItems, trimTotalDimensionKeep)
	trimDimensionData(s.today, maxBucketDimensionItems, trimBucketDimensionKeep)

	lastConnsClone := make(map[string]lastConnTrafficPersist, len(s.lastConnections))
	for k, v := range s.lastConnections {
		lastConnsClone[k] = lastConnTrafficPersist{
			Download:    v.Download,
			Upload:      v.Upload,
			ClientID:    v.meta.clientID,
			Destination: v.meta.destination,
			Process:     v.meta.process,
			Node:        v.meta.node,
			Rule:        v.meta.rule,
		}
	}

	data := currentTrafficPersistData{
		Total:             s.total.clone(),
		Today:             s.today.clone(),
		TodayDate:         s.todayDate,
		LastConnections:   lastConnsClone,
		StartTime:         s.startTime,
		AutoCleanInterval: s.autoCleanInterval,
	}
	s.dirty = false
	currentFile := s.currentFile
	s.mu.Unlock()

	if err := atomicWriteJSON(currentFile, data); err != nil {
		debugLogf("traffic", "failed to save current traffic data: %v", err)
		return
	}
	debugLogf("traffic", "saved current traffic snapshot to disk (total_domains=%d, today_domains=%d)",
		len(data.Total.Domains), len(data.Today.Domains))
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
	s.checkDayRotationLocked(now)

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
				// 长连接直接复用元数据，避免重复调用字符串处理
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

			// 极简两级记账：仅计入全量总桶与当天桶
			s.total.add(&meta, diffUp, diffDown, isNew)
			s.today.add(&meta, diffUp, diffDown, isNew)

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
		var initCount int64
		if isNew {
			initCount = 1
		}
		item = &TrafficItem{Name: name, Count: initCount}
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

func formatRankResult(res TrafficRankResult, items []TrafficItem, req TrafficRankRequest, summaryDown, summaryUp, summaryCount int64) TrafficRankResult {
	orderDesc := true
	if req.OrderBy != "" {
		orderDesc = req.OrderDesc
	}

	res.TotalDownload = summaryDown
	res.TotalUpload = summaryUp
	res.TotalCount = summaryCount

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
	dimension := req.Dimension
	timeRange := strings.ToLower(strings.TrimSpace(req.TimeRange))
	if timeRange == "" {
		timeRange = "all"
	}

	pageNum := req.PageNum
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 200
	}

	now := time.Now()
	s.checkDayRotation(now)

	// 1. 查询全部（all）或今天（today）：直接极速读内存，零跨桶合并！
	if timeRange == "all" || timeRange == "today" {
		s.mu.RLock()
		res := TrafficRankResult{
			List:      make([]TrafficItem, 0),
			PageNum:   pageNum,
			PageSize:  pageSize,
			StartTime: s.startTime,
		}
		var targetMap map[string]*TrafficItem
		var summaryDown, summaryUp, summaryCount int64
		if timeRange == "today" {
			targetMap = s.today.getMap(dimension)
			summaryDown = s.today.TotalDownload
			summaryUp = s.today.TotalUpload
			summaryCount = s.today.TotalCount
		} else {
			targetMap = s.total.getMap(dimension)
			summaryDown = s.total.TotalDownload
			summaryUp = s.total.TotalUpload
			summaryCount = s.total.TotalCount
		}
		items := make([]TrafficItem, 0, len(targetMap))
		for _, item := range targetMap {
			if item != nil {
				items = append(items, *item)
			}
		}
		s.mu.RUnlock()
		return formatRankResult(res, items, req, summaryDown, summaryUp, summaryCount)
	}

	// 2. 查询 7d 或 30d：先检查短缓存（TTL 2.5秒，消除前端 1s 高频轮询下的重复合并计算）
	cacheKey := fmt.Sprintf("%s:%s:%s:%t:%d:%d",
		dimension, timeRange, req.OrderBy, req.OrderDesc, pageNum, pageSize)

	s.queryCacheMu.Lock()
	if cached, ok := s.queryCache[cacheKey]; ok {
		if now.Before(cached.expiresAt) {
			res := cached.result
			s.queryCacheMu.Unlock()
			return res
		}
		delete(s.queryCache, cacheKey)
	}
	s.queryCacheMu.Unlock()

	// 3. 构建多天合并数据：以当天的 today 为基准
	s.mu.RLock()
	startTime := s.startTime
	merged := make(map[string]*TrafficItem)

	todayMap := s.today.getMap(dimension)
	for _, item := range todayMap {
		addTrafficItemToMerged(merged, item)
	}
	summaryDown := s.today.TotalDownload
	summaryUp := s.today.TotalUpload
	summaryCount := s.today.TotalCount
	s.mu.RUnlock()

	// 确定历史回溯天数
	daysBack := 6
	if timeRange == "30d" {
		daysBack = 29
	}

	for i := 1; i <= daysBack; i++ {
		dateKey := now.AddDate(0, 0, -i).Format("20060102")
		bucket := s.readDailyBucket(dateKey)
		if bucket != nil {
			summaryDown += bucket.TotalDownload
			summaryUp += bucket.TotalUpload
			summaryCount += bucket.TotalCount

			m := bucket.getMap(dimension)
			for _, item := range m {
				addTrafficItemToMerged(merged, item)
			}
		}
	}

	items := make([]TrafficItem, 0, len(merged))
	for _, item := range merged {
		if item != nil {
			items = append(items, *item)
		}
	}

	res := TrafficRankResult{
		List:      make([]TrafficItem, 0),
		PageNum:   pageNum,
		PageSize:  pageSize,
		StartTime: startTime,
	}
	result := formatRankResult(res, items, req, summaryDown, summaryUp, summaryCount)

	// 写入短缓存（2.5 秒有效，仅占用不足 10KB 内存，过期或跨天时主动淘汰）
	s.queryCacheMu.Lock()
	s.queryCache[cacheKey] = cachedRankResult{
		result:    result,
		expiresAt: now.Add(2500 * time.Millisecond),
	}
	s.queryCacheMu.Unlock()

	return result
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
