<template>
  <div class="base-container w-full backdrop-blur-none!">
    <!-- Header -->
    <div
      class="surface flex items-center justify-between p-4 max-sm:flex-col max-sm:items-start max-sm:gap-2"
    >
      <div
        class="text-base-content/60 flex items-center gap-2 text-xs font-semibold tracking-wider uppercase"
      >
        {{ $t('totalConnections') }}
        <button
          class="btn btn-ghost btn-xs btn-circle"
          @click="showClearDialog = true"
        >
          <TrashIcon class="h-3.5 w-3.5" />
        </button>
        <QuestionMarkCircleIcon
          class="h-3.5 w-3.5 cursor-pointer"
          @mouseenter="showTip($event, totalConnectionsTip)"
        />
      </div>
      <!-- v-memo: avoid re-rendering the selects on every connection poll (flicker on firefox) -->
      <div
        v-memo="[aggregationType, trafficTimeRange, trafficAutoCleanInterval, locale]"
        class="flex items-center gap-2 max-sm:flex-col max-sm:items-start"
      >
        <div class="flex items-center gap-2">
          <span class="text-base-content/60 text-xs">{{ $t('aggregateBy') }}</span>
          <SelectInput
            v-model="aggregationType"
            class="select select-bordered select-sm w-32"
            :options="[
              { value: ConnectionHistoryType.SourceIP, label: $t('aggregateBySourceIP') },
              {
                value: ConnectionHistoryType.Destination,
                label: $t('aggregateByDestination'),
              },
              { value: ConnectionHistoryType.Process, label: $t('aggregateByProcess') },
              { value: ConnectionHistoryType.Outbound, label: $t('aggregateByOutbound') },
              { value: ConnectionHistoryType.ProxyGroup, label: $t('aggregateByProxyGroup') },
            ]"
          />
        </div>
        <div class="flex items-center gap-2">
          <span class="text-base-content/60 text-xs">{{ $t('trafficTimeRange') }}</span>
          <SelectInput
            v-model="trafficTimeRange"
            class="select select-bordered select-sm w-32"
            :options="[
              { value: 'all', label: $t('trafficTimeRangeAll') },
              { value: '24h', label: $t('trafficTimeRange24h') },
              { value: '7d', label: $t('trafficTimeRange7d') },
              { value: '30d', label: $t('trafficTimeRange30d') },
            ]"
          />
        </div>
        <div class="flex items-center gap-2">
          <span class="text-base-content/60 text-xs">{{ $t('autoCleanupInterval') }}</span>
          <SelectInput
            :model-value="trafficAutoCleanInterval"
            class="select select-bordered select-sm w-28"
            :options="[
              { value: 'never', label: $t('autoCleanupIntervalNever') },
              { value: 'week', label: $t('autoCleanupIntervalWeek') },
              { value: 'month', label: $t('autoCleanupIntervalMonth') },
              { value: 'quarter', label: $t('autoCleanupIntervalQuarter') },
            ]"
            @update:model-value="updateTrafficAutoClean(String($event))"
          />
        </div>
      </div>
    </div>

    <!-- Stats grid -->
    <div class="surface grid grid-cols-2 gap-3 px-4 pb-4 sm:grid-cols-5">
      <div class="bg-base-200/30 flex flex-col gap-1.5 rounded-xl p-4">
        <div class="text-base-content/60 text-xs font-semibold tracking-wider uppercase">
          {{ aggregateSourceLabel }}
        </div>
        <div class="text-2xl font-extralight tabular-nums">{{ aggregateSourceCount }}</div>
      </div>
      <div class="bg-base-200/30 flex flex-col gap-1.5 rounded-xl p-4">
        <div class="text-base-content/60 text-xs font-semibold tracking-wider uppercase">
          {{ t('totalTraffic') }}
        </div>
        <div class="text-2xl font-extralight tabular-nums">
          {{ prettyBytesHelper(totalStats.download + totalStats.upload) }}
        </div>
      </div>
      <div class="bg-base-200/30 flex flex-col gap-1.5 rounded-xl p-4">
        <div class="text-base-content/60 text-xs font-semibold tracking-wider uppercase">
          {{ t('download') }}
        </div>
        <div class="text-2xl font-extralight tabular-nums">
          {{ prettyBytesHelper(totalStats.download) }}
        </div>
      </div>
      <div class="bg-base-200/30 flex flex-col gap-1.5 rounded-xl p-4">
        <div class="text-base-content/60 text-xs font-semibold tracking-wider uppercase">
          {{ t('upload') }}
        </div>
        <div class="text-2xl font-extralight tabular-nums">
          {{ prettyBytesHelper(totalStats.upload) }}
        </div>
      </div>

      <div class="bg-base-200/30 flex flex-col gap-1.5 rounded-xl p-4">
        <div class="text-base-content/60 text-xs font-semibold tracking-wider uppercase">
          {{ t('connectionCount') }}
        </div>
        <div class="text-2xl font-extralight tabular-nums">{{ totalStats.count }}</div>
      </div>
    </div>
    <div
      ref="parentRef"
      class="h-96 overflow-auto"
    >
      <div :style="{ height: `${totalSize}px` }">
        <table class="table-sm table w-full rounded-none">
          <thead class="bg-base-200 sticky top-0 z-10">
            <tr>
              <th
                v-for="header in tanstackTable.getHeaderGroups()[0]?.headers"
                :key="header.id"
                class="cursor-pointer select-none"
                @click="header.column.getToggleSortingHandler()?.($event)"
              >
                <div class="flex items-center gap-1">
                  <FlexRender
                    v-if="!header.isPlaceholder"
                    :render="header.column.columnDef.header"
                    :props="header.getContext()"
                  />
                  <ArrowUpCircleIcon
                    v-if="header.column.getIsSorted() === 'asc'"
                    class="h-4 w-4"
                  />
                  <ArrowDownCircleIcon
                    v-if="header.column.getIsSorted() === 'desc'"
                    class="h-4 w-4"
                  />
                </div>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(virtualRow, index) in virtualRows"
              :key="virtualRow.key.toString()"
              :style="{
                height: `${virtualRow.size}px`,
                transform: `translateY(${virtualRow.start - index * virtualRow.size}px)`,
              }"
              class="hover:bg-primary/85! hover:text-primary-content whitespace-nowrap"
              :class="virtualRow.index % 2 === 1 && 'table-row-stripe'"
            >
              <td
                v-for="cell in rows[virtualRow.index].getVisibleCells()"
                :key="cell.id"
                class="text-sm"
              >
                <FlexRender
                  :render="cell.column.columnDef.cell"
                  :props="cell.getContext()"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <DialogWrapper
      v-model="showClearDialog"
      :title="$t('clearConnectionHistory')"
    >
      <div class="flex flex-col gap-4 p-2">
        <p class="text-sm">
          {{ $t('clearConnectionHistoryConfirm') }}
        </p>
        <div class="flex justify-end gap-2">
          <button
            class="btn btn-sm"
            @click="showClearDialog = false"
          >
            {{ $t('cancel') }}
          </button>
          <button
            class="btn btn-error btn-sm"
            @click="handleClearHistory"
          >
            {{ $t('confirm') }}
          </button>
        </div>
      </div>
    </DialogWrapper>
  </div>
</template>

<script setup lang="ts">
import { ConnectionHistoryType } from '@/helper/indexeddb'
import SelectInput from '@/components/common/SelectInput.vue'
import { showNotification } from '@/helper/notification'
import { getIPLabelFromMap } from '@/helper/sourceip'
import { useStorage } from '@/helper/storage'
import { useTooltip } from '@/helper/tooltip'
import { prettyBytesHelper } from '@/helper/utils'
import {
  aggregatedDataMap,
  clearConnectionHistory,
  fetchDimensionHistory,
  startConnectionHistoryPolling,
  stopConnectionHistoryPolling,
  trafficAutoCleanInterval,
  trafficOverallStats,
  trafficStatsStartTime,
  updateTrafficAutoClean,
  type TrafficTimeRange,
} from '@/store/connHistory'
import {
  ArrowDownCircleIcon,
  ArrowUpCircleIcon,
  QuestionMarkCircleIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'
import {
  FlexRender,
  getCoreRowModel,
  getSortedRowModel,
  useVueTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/vue-table'
import { useVirtualizer } from '@tanstack/vue-virtual'
import dayjs from 'dayjs'
import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import DialogWrapper from '../common/DialogWrapper.vue'
import ProxyName from '../proxies/ProxyName.vue'

const { t, locale } = useI18n()
const { showTip } = useTooltip()

interface ConnectionHistoryData {
  key: string
  download: number
  upload: number
  count: number
}

const aggregationType = useStorage<ConnectionHistoryType>(
  'cache/connection-history-aggregation-type',
  ConnectionHistoryType.SourceIP,
)
const trafficTimeRange = useStorage<TrafficTimeRange>(
  'cache/connection-history-time-range',
  'all',
)
const historicalData = computed(() => aggregatedDataMap.value[aggregationType.value])
const aggregatedData = computed<ConnectionHistoryData[]>(() => {
  return historicalData.value || []
})

const totalStats = computed(() => trafficOverallStats.value)

const aggregateSourceCount = computed(() => trafficOverallStats.value.totalItems)

const aggregateSourceLabel = computed(() => {
  if (aggregationType.value === ConnectionHistoryType.SourceIP) {
    return t('sourceIP')
  } else if (aggregationType.value === ConnectionHistoryType.Destination) {
    return t('host')
  } else if (aggregationType.value === ConnectionHistoryType.Process) {
    return t('process')
  } else if (aggregationType.value === ConnectionHistoryType.ProxyGroup) {
    return t('proxyGroup')
  } else {
    return t('outbound')
  }
})

const columns = computed<ColumnDef<ConnectionHistoryData>[]>(() => {
  const keyColumn: ColumnDef<ConnectionHistoryData> = {
    header: () => aggregateSourceLabel.value,
    id: 'key',
    accessorFn: (row) => row.key,
    cell: ({ row }) => {
      if (aggregationType.value === ConnectionHistoryType.SourceIP) {
        return getIPLabelFromMap(row.original.key)
      } else if (aggregationType.value === ConnectionHistoryType.Destination) {
        return row.original.key
      } else if (aggregationType.value === ConnectionHistoryType.Process) {
        return row.original.key
      } else {
        return h(ProxyName, { name: row.original.key })
      }
    },
  }

  return [
    keyColumn,
    {
      header: () => t('download'),
      id: 'download',
      accessorFn: (row) => row.download,
      cell: ({ row }) => prettyBytesHelper(row.original.download),
      sortingFn: (prev, next) => prev.original.download - next.original.download,
      sortDescFirst: true,
    },
    {
      header: () => t('upload'),
      id: 'upload',
      accessorFn: (row) => row.upload,
      cell: ({ row }) => prettyBytesHelper(row.original.upload),
      sortingFn: (prev, next) => prev.original.upload - next.original.upload,
      sortDescFirst: true,
    },
    {
      header: () => t('totalTraffic'),
      id: 'total',
      accessorFn: (row) => row.download + row.upload,
      cell: ({ row }) => prettyBytesHelper(row.original.download + row.original.upload),
      sortingFn: (prev, next) =>
        prev.original.download +
        prev.original.upload -
        (next.original.download + next.original.upload),
      sortDescFirst: true,
    },
    {
      header: () => t('connectionCount'),
      id: 'count',
      accessorFn: (row) => row.count,
      cell: ({ row }) => row.original.count.toString(),
      sortingFn: (prev, next) => prev.original.count - next.original.count,
      sortDescFirst: true,
    },
  ]
})

const sorting = useStorage<SortingState>('cache/connection-history-sorting', [
  { id: 'download', desc: true },
])

const tanstackTable = useVueTable({
  get data() {
    return aggregatedData.value
  },
  get columns() {
    return columns.value
  },
  state: {
    get sorting() {
      return sorting.value
    },
  },
  onSortingChange: (updater) => {
    if (typeof updater === 'function') {
      sorting.value = updater(sorting.value)
    } else {
      sorting.value = updater
    }
  },
  getSortedRowModel: getSortedRowModel(),
  getCoreRowModel: getCoreRowModel(),
})

const rows = computed(() => {
  return tanstackTable.getRowModel().rows
})

const parentRef = ref<HTMLElement | null>(null)
const rowVirtualizerOptions = computed(() => {
  return {
    count: rows.value.length,
    getScrollElement: () => parentRef.value,
    estimateSize: () => 36,
    overscan: 10,
  }
})

const rowVirtualizer = useVirtualizer(rowVirtualizerOptions)
const virtualRows = computed(() => rowVirtualizer.value.getVirtualItems())
const totalSize = computed(() => rowVirtualizer.value.getTotalSize() + 24)

const showClearDialog = ref(false)

const totalConnectionsTip = computed(() => {
  if (trafficTimeRange.value === '24h') {
    return t('trafficTimeRange24h')
  }
  if (trafficTimeRange.value === '7d') {
    return t('trafficTimeRange7d')
  }
  if (trafficTimeRange.value === '30d') {
    return t('trafficTimeRange30d')
  }
  const dayjsTime = dayjs(trafficStatsStartTime.value)
  return t('totalConnectionsTip', {
    statsStartTime: `${dayjsTime.format('YYYY-MM-DD HH:mm')} (${dayjsTime.fromNow()})`,
  })
})

const handleClearHistory = async () => {
  try {
    await clearConnectionHistory()
    showClearDialog.value = false
    showNotification({
      content: t('clearConnectionHistorySuccess'),
      type: 'alert-success',
    })
  } catch (error) {
    console.error('Failed to clear connection history:', error)
    showNotification({
      content: `${t('saveFailed')}: ${error}`,
      type: 'alert-error',
    })
  }
}

watch(aggregationType, (newType) => {
  fetchDimensionHistory(newType, trafficTimeRange.value, true)
})

watch(trafficTimeRange, (newRange) => {
  fetchDimensionHistory(aggregationType.value, newRange, true)
})

watch(
  sorting,
  () => {
    fetchDimensionHistory(aggregationType.value, trafficTimeRange.value, true)
  },
  { deep: true },
)

onMounted(() => {
  startConnectionHistoryPolling(aggregationType, trafficTimeRange, sorting)
})

onUnmounted(() => {
  stopConnectionHistoryPolling()
})
</script>
