import * as CoreService from '@/../bindings/zashdesktop/coreservice'
import { backendProbe } from '@/assembly/version'
import {
  ConnectionHistoryType,
  type ConnectionHistoryData,
} from '@/helper/indexeddb'
import type { Connection } from '@/types'
import { ref, shallowRef, type Ref } from 'vue'

const allHistoryTypes: ConnectionHistoryType[] = [
  ConnectionHistoryType.SourceIP,
  ConnectionHistoryType.Destination,
  ConnectionHistoryType.Process,
  ConnectionHistoryType.Outbound,
  ConnectionHistoryType.ProxyGroup,
]

const dimensionMap: Record<ConnectionHistoryType, string> = {
  [ConnectionHistoryType.SourceIP]: 'clients',
  [ConnectionHistoryType.Destination]: 'domains',
  [ConnectionHistoryType.Process]: 'processes',
  [ConnectionHistoryType.Outbound]: 'nodes',
  [ConnectionHistoryType.ProxyGroup]: 'rules',
}

const emptyView = (): Record<ConnectionHistoryType, ConnectionHistoryData[]> => ({
  [ConnectionHistoryType.SourceIP]: [],
  [ConnectionHistoryType.Destination]: [],
  [ConnectionHistoryType.Process]: [],
  [ConnectionHistoryType.Outbound]: [],
  [ConnectionHistoryType.ProxyGroup]: [],
})

// 展示态: 直接由 Go 后端 GetTrafficRank 填充
export const aggregatedDataMap = shallowRef(emptyView())

export type TrafficTimeRange = 'all' | '24h' | '7d' | '30d'

// 后端托管的统计起始时间与自动清理周期
export const trafficStatsStartTime = ref<number>(Date.now())
export const trafficAutoCleanInterval = ref<string>('month')

export const fetchTrafficMeta = async () => {
  try {
    const meta = await CoreService.GetTrafficMeta()
    if (meta) {
      if (meta.startTime) trafficStatsStartTime.value = meta.startTime
      if (meta.autoCleanInterval) trafficAutoCleanInterval.value = meta.autoCleanInterval
    }
  } catch {
    // 后端未就绪时静默
  }
}

export const updateTrafficAutoClean = async (interval: string) => {
  try {
    await CoreService.SetTrafficAutoClean(interval)
    trafficAutoCleanInterval.value = interval
  } catch (e) {
    console.error('Failed to set auto clean interval:', e)
  }
}

let activeTypeRef: Ref<ConnectionHistoryType> | null = null
let activeRangeRef: Ref<TrafficTimeRange> | null = null
let activeSortingRef: Ref<Array<{ id: string; desc: boolean }>> | null = null
let refreshTimer: ReturnType<typeof setInterval> | null = null
let fetchSeq = 0
let isFetching = false
let isVisibilityBound = false

// 对齐活跃连接的 1 秒高频节拍，保证实时跳动体感
const POLL_INTERVAL_MS = 1000

export const fetchDimensionHistory = async (
  type: ConnectionHistoryType,
  timeRange?: TrafficTimeRange,
  force = false,
) => {
  // 1. 页面不可见（隐藏在托盘、最小化或锁屏）时严禁发起到后端的 IPC 请求
  if (typeof document !== 'undefined' && document.hidden) {
    return
  }
  // 2. 内核未连通时静默，避免对关闭的端口产生无谓拉取
  if (backendProbe.value?.status !== 'connected') {
    return
  }
  // 3. 轮询期间防止请求堆叠；用户主动点击切换时可通过 force 穿透发起
  if (isFetching && !force) {
    return
  }
  const dim = dimensionMap[type]
  if (!dim) return

  const range = timeRange || activeRangeRef?.value || 'all'
  const currentSort = activeSortingRef?.value?.[0]
  const seq = ++fetchSeq

  isFetching = true
  try {
    const res = await CoreService.GetTrafficRank({
      dimension: dim,
      timeRange: range,
      orderBy: currentSort?.id,
      orderDesc: currentSort?.desc ?? true,
      pageNum: 1,
      pageSize: 200,
    })
    // 竞态保护：仅当没有更新的请求发出时才应用数据
    if (seq === fetchSeq && res && res.list) {
      if (res.startTime) {
        trafficStatsStartTime.value = res.startTime
      }
      aggregatedDataMap.value = {
        ...aggregatedDataMap.value,
        [type]: res.list.map((item) => ({
          key: item.name,
          download: item.down,
          upload: item.up,
          count: item.count,
        })),
      }
    }
  } catch {
    // 后端未运行或返回错误时静默
  } finally {
    if (seq === fetchSeq) {
      isFetching = false
    }
  }
}

const setupTimer = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  refreshTimer = setInterval(() => {
    if (activeTypeRef && typeof document !== 'undefined' && !document.hidden) {
      fetchDimensionHistory(activeTypeRef.value, activeRangeRef?.value)
    }
  }, POLL_INTERVAL_MS)
}

const onVisibilityChange = () => {
  if (typeof document === 'undefined') return
  if (document.hidden) {
    // 页面切到后台：彻底销毁定时器，保证 JS 线程零唤醒、零轮询！
    if (refreshTimer) {
      clearInterval(refreshTimer)
      refreshTimer = null
    }
  } else {
    // 页面切回前台：立即获取最新数据并恢复定时器
    if (activeTypeRef) {
      fetchDimensionHistory(activeTypeRef.value, activeRangeRef?.value)
      setupTimer()
    }
  }
}

export const fetchHistoryFromBackend = async () => {
  if (activeTypeRef) {
    await fetchDimensionHistory(activeTypeRef.value, activeRangeRef?.value)
  }
}

export const startConnectionHistoryPolling = (
  currentType: Ref<ConnectionHistoryType>,
  currentRange?: Ref<TrafficTimeRange>,
  currentSorting?: Ref<Array<{ id: string; desc: boolean }>>,
) => {
  activeTypeRef = currentType
  activeRangeRef = currentRange || null
  activeSortingRef = currentSorting || null

  // 立即同步后端元数据与拉取当前视图
  void fetchTrafficMeta()
  fetchDimensionHistory(currentType.value, currentRange?.value)

  // 绑定能见度监听（挂托盘直接销毁定时器）
  if (typeof document !== 'undefined' && !isVisibilityBound) {
    document.addEventListener('visibilitychange', onVisibilityChange)
    isVisibilityBound = true
  }

  // 仅在当前可见时开启定时器
  if (typeof document === 'undefined' || !document.hidden) {
    setupTimer()
  }
}

export const stopConnectionHistoryPolling = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  if (typeof document !== 'undefined' && isVisibilityBound) {
    document.removeEventListener('visibilitychange', onVisibilityChange)
    isVisibilityBound = false
  }
  activeTypeRef = null
  activeRangeRef = null
  activeSortingRef = null
}

export const initAggregatedDataMap = () => {
  // 保持兼容性接口：无需全局无休止开启轮询，改由 Overview/ConnectionHistory 组件按需激活
}

export const stopConnectionHistory = () => {
  stopConnectionHistoryPolling()
}

// 禁用原有前端关闭连接时的累加与 IndexedDB 写入（已全面由 Go 后端接管）
export const saveConnectionHistory = (_newClosedConnections: Connection[]) => {
  // no-op
}

export const clearConnectionHistory = async () => {
  try {
    await CoreService.ClearTrafficData()
    trafficStatsStartTime.value = Date.now()
  } catch (e) {
    console.error('Failed to clear traffic in backend:', e)
  }
  aggregatedDataMap.value = emptyView()
}

// 兼容保留接口，直接返回空，避免前端在 activeConnections 上重复累加
export const aggregateConnections = (
  _connections: Connection[],
  _type: ConnectionHistoryType,
): ConnectionHistoryData[] => {
  return []
}

export const mergeAggregatedData = (
  historical: ConnectionHistoryData[],
  _newData: ConnectionHistoryData[],
): ConnectionHistoryData[] => {
  return historical
}
