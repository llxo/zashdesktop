import * as CoreService from '@/../bindings/zashdesktop/coreservice'
import {
  ConnectionHistoryType,
  type ConnectionHistoryData,
} from '@/helper/indexeddb'
import type { Connection } from '@/types'
import { shallowRef, type Ref } from 'vue'

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

let activeTypeRef: Ref<ConnectionHistoryType> | null = null
let refreshTimer: ReturnType<typeof setInterval> | null = null
let isFetching = false

export const fetchDimensionHistory = async (type: ConnectionHistoryType) => {
  if (typeof document !== 'undefined' && document.hidden) {
    return
  }
  if (isFetching) {
    return
  }
  const dim = dimensionMap[type]
  if (!dim) return

  isFetching = true
  try {
    const res = await CoreService.GetTrafficRank({
      dimension: dim,
      pageNum: 1,
      pageSize: 200,
    })
    if (res && res.list) {
      aggregatedDataMap.value = {
        ...aggregatedDataMap.value,
        [type]: res.list.map((item) => ({
          key: item.name,
          download: item.down,
          upload: item.up,
          count: item.count || 1,
        })),
      }
    }
  } catch {
    // 后端未运行或返回错误时静默
  } finally {
    isFetching = false
  }
}

export const fetchHistoryFromBackend = async () => {
  if (activeTypeRef) {
    await fetchDimensionHistory(activeTypeRef.value)
  }
}

export const startConnectionHistoryPolling = (currentType: Ref<ConnectionHistoryType>) => {
  activeTypeRef = currentType
  fetchDimensionHistory(currentType.value)
  if (!refreshTimer) {
    refreshTimer = setInterval(() => {
      if (activeTypeRef) {
        fetchDimensionHistory(activeTypeRef.value)
      }
    }, 3000)
  }
}

export const stopConnectionHistoryPolling = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  activeTypeRef = null
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
