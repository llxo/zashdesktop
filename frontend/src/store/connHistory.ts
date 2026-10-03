import * as CoreService from '@/../bindings/zashdesktop/coreservice'
import {
  ConnectionHistoryType,
  type ConnectionHistoryData,
} from '@/helper/indexeddb'
import type { Connection } from '@/types'
import { shallowRef } from 'vue'

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

let refreshTimer: ReturnType<typeof setInterval> | null = null

export const fetchHistoryFromBackend = async () => {
  try {
    const next = { ...aggregatedDataMap.value }
    await Promise.all(
      allHistoryTypes.map(async (type) => {
        const dim = dimensionMap[type]
        const res = await CoreService.GetTrafficRank({
          dimension: dim,
          pageNum: 1,
          pageSize: 500,
        })
        if (res && res.list) {
          next[type] = res.list.map((item) => ({
            key: item.name,
            download: item.down,
            upload: item.up,
            count: item.hits ?? item.count ?? 1,
          }))
        }
      }),
    )
    aggregatedDataMap.value = next
  } catch {
    // 后端未运行或返回错误时静默
  }
}

export const initAggregatedDataMap = () => {
  fetchHistoryFromBackend()
  if (!refreshTimer) {
    refreshTimer = setInterval(fetchHistoryFromBackend, 3000)
  }
}

export const stopConnectionHistory = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
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
