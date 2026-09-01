<template>
  <DialogWrapper
    v-model="isOpen"
    :title="$t('customFont')"
  >
    <div class="space-y-4">
      <div class="text-base-content/70 text-xs">
        {{ $t('customFontTip') }}
      </div>

      <!-- 字体输入框 -->
      <div class="form-control">
        <label class="label pb-1">
          <span class="label-text text-xs font-medium">{{ $t('fontFamily') || '字体名称' }}</span>
        </label>
        <TextInput
          v-model="fontInput"
          :placeholder="$t('customFontPlaceholder')"
          class="w-full"
          @keydown.enter="handleSave"
        />
      </div>

      <!-- 实时效果预览卡片 -->
      <div class="bg-base-200/50 border-base-content/10 rounded-xl border p-3">
        <div class="text-base-content/50 mb-1.5 text-xs font-medium">
          {{ $t('preview') || '实时预览' }}
        </div>
        <div
          class="bg-base-100 rounded-lg p-3 text-sm leading-relaxed shadow-xs select-none"
          :style="{ fontFamily: previewFamily }"
        >
          <div class="font-medium text-base">
            zashdesktop 代理核心控制面板
          </div>
          <div class="text-base-content/80 mt-1 text-xs">
            节点延迟 28ms · 吞吐速率 128.5 MB/s (0123456789)
          </div>
          <div class="mt-2 text-xs">
            🇭🇰 香港专线 · 🇯🇵 日本高速 · 🇺🇸 备用节点 · 🇸🇬 狮城游戏
          </div>
        </div>
      </div>

      <!-- 快捷预设标签 -->
      <div>
        <div class="text-base-content/50 mb-1.5 text-xs font-medium">
          {{ $t('recommendedFonts') }}
        </div>
        <div class="flex flex-wrap gap-1.5">
          <button
            v-for="item in quickPresets"
            :key="item.value"
            type="button"
            class="badge badge-sm badge-ghost cursor-pointer hover:badge-primary transition-colors"
            @click="fontInput = item.value"
          >
            {{ item.label }}
          </button>
        </div>
      </div>

      <!-- 底部动作栏 -->
      <div class="border-base-content/10 flex items-center justify-between border-t pt-3">
        <button
          type="button"
          class="btn btn-sm btn-ghost"
          @click="handleReset"
        >
          {{ $t('reset') }}
        </button>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="btn btn-sm"
            @click="isOpen = false"
          >
            {{ $t('cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-sm btn-primary"
            @click="handleSave"
          >
            {{ $t('apply') || $t('save') }}
          </button>
        </div>
      </div>
    </div>
  </DialogWrapper>
</template>

<script setup lang="ts">
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import TextInput from '@/components/common/TextInput.vue'
import { emoji, font } from '@/store/settings'
import { computed, ref, watch } from 'vue'

const isOpen = defineModel<boolean>('value', {
  default: false,
})

const fontInput = ref('')

const quickPresets = [
  { label: '系统默认', value: '' },
  { label: '微软雅黑', value: 'Microsoft YaHei' },
  { label: 'Segoe UI', value: 'Segoe UI' },
  { label: '鸿蒙黑体', value: 'HarmonyOS Sans SC' },
  { label: '苹方', value: 'PingFang SC' },
  { label: '思源黑体', value: 'Source Han Sans SC' },
  { label: '霞鹜文楷', value: 'LXGW WenKai' },
  { label: '得意黑', value: 'Smiley Sans' },
  { label: 'Cascadia Code', value: 'Cascadia Code' },
  { label: 'JetBrains Mono', value: 'JetBrains Mono' },
]

const previewFamily = computed(() => {
  const f = fontInput.value.trim()
  const emojiFont = emoji.value === 'noto-color-emoji' ? 'NotoEmoji' : 'Twemoji'
  if (!f) {
    return `'${emojiFont}', system-ui, -apple-system, sans-serif`
  }
  return `'${f}', '${emojiFont}', system-ui, -apple-system, sans-serif`
})

watch(isOpen, (open) => {
  if (open) {
    fontInput.value = font.value || ''
  }
})

const handleReset = () => {
  fontInput.value = ''
  font.value = ''
  isOpen.value = false
}

const handleSave = () => {
  font.value = fontInput.value.trim()
  isOpen.value = false
}
</script>
