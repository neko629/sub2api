<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.platformQuota.title')"
    width="wide"
    @close="$emit('close')"
  >
    <div v-if="user" class="space-y-4">
      <div
        v-if="hasActiveSubscription"
        class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      >
        {{ t('admin.users.platformQuota.subscriptionWarning') }}
      </div>
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.users.platformQuota.subtitle', { email: user.email }) }}
      </p>
      <div v-if="loading" class="py-10 text-center text-gray-500">{{ t('common.loading') }}</div>
      <div v-else class="overflow-x-auto">
        <!-- 5h / 周窗口若跟随基准账号，刷新时刻由上游决定（可能不是整点），需明确告知 -->
        <div
          v-if="syncedPlatforms.length > 0"
          class="mb-3 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-xs leading-relaxed text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        >
          {{ t('admin.users.platformQuota.syncedNotice', { platforms: syncedPlatforms.join(', ') }) }}
        </div>
        <table class="min-w-full text-sm">
          <thead>
            <tr class="border-b border-gray-200 text-gray-700 dark:border-dark-700 dark:text-gray-300">
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.platform') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.fiveHour') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.daily') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.weekly') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.monthly') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.usage') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in quotas" :key="row.platform" class="border-b border-gray-100 dark:border-dark-800">
              <td class="px-3 py-2 font-mono text-gray-900 dark:text-white">{{ row.platform }}</td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <input
                    v-model.number="row.five_hour_limit_usd"
                    type="number"
                    min="0"
                    step="0.01"
                    class="input w-24"
                    :placeholder="t('admin.users.platformQuota.placeholder')"
                    :title="windowTooltip(row, 'five_hour')"
                  />
                  <span
                    v-if="row.five_hour_window_source === 'account'"
                    class="rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold text-amber-700 dark:bg-amber-500/20 dark:text-amber-200"
                    :title="windowTooltip(row, 'five_hour')"
                  >{{ t('admin.users.platformQuota.syncedBadge') }}</span>
                  <button
                    type="button"
                    class="text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50"
                    :disabled="!!resetting[`${row.platform}.five_hour`]"
                    :title="t('admin.users.platformQuota.reset.button')"
                    @click="onReset(row.platform, 'five_hour')"
                  >↻</button>
                </div>
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <input
                    v-model.number="row.daily_limit_usd"
                    type="number"
                    min="0"
                    step="0.01"
                    class="input w-24"
                    :placeholder="t('admin.users.platformQuota.placeholder')"
                  />
                  <button
                    type="button"
                    class="text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50"
                    :disabled="!!resetting[`${row.platform}.daily`]"
                    :title="t('admin.users.platformQuota.reset.button')"
                    @click="onReset(row.platform, 'daily')"
                  >↻</button>
                </div>
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <input
                    v-model.number="row.weekly_limit_usd"
                    type="number"
                    min="0"
                    step="0.01"
                    class="input w-24"
                    :placeholder="t('admin.users.platformQuota.placeholder')"
                  />
                  <button
                    type="button"
                    class="text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50"
                    :disabled="!!resetting[`${row.platform}.weekly`]"
                    :title="t('admin.users.platformQuota.reset.button')"
                    @click="onReset(row.platform, 'weekly')"
                  >↻</button>
                  <span
                    v-if="row.weekly_window_source === 'account'"
                    class="rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold text-amber-700 dark:bg-amber-500/20 dark:text-amber-200"
                    :title="windowTooltip(row, 'weekly')"
                  >{{ t('admin.users.platformQuota.syncedBadge') }}</span>
                </div>
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <input
                    v-model.number="row.monthly_limit_usd"
                    type="number"
                    min="0"
                    step="0.01"
                    class="input w-24"
                    :placeholder="t('admin.users.platformQuota.placeholder')"
                  />
                  <button
                    type="button"
                    class="text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50"
                    :disabled="!!resetting[`${row.platform}.monthly`]"
                    :title="t('admin.users.platformQuota.reset.button')"
                    @click="onReset(row.platform, 'monthly')"
                  >↻</button>
                </div>
              </td>
              <td class="px-3 py-2 text-xs text-gray-500 dark:text-gray-400">
                {{ formatUsage(row.five_hour_usage_usd) }} / {{ formatUsage(row.daily_usage_usd) }} / {{ formatUsage(row.weekly_usage_usd) }} / {{ formatUsage(row.monthly_usage_usd) }}
              </td>
            </tr>
          </tbody>
        </table>
        <p class="mt-3 text-xs text-gray-500">{{ t('admin.users.platformQuota.hint') }}</p>
        <div class="mt-3">
          <button type="button" class="btn btn-secondary text-sm" @click="onClearAll">
            {{ t('admin.users.platformQuota.clearAll') }}
          </button>
        </div>
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">
          {{ t('admin.users.platformQuota.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="submitting || loading" @click="onSave">
          {{ submitting ? t('admin.users.platformQuota.saving') : t('admin.users.platformQuota.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type {
  AdminUser,
  PlatformQuotaItem,
  PlatformQuotaPlatform,
  PlatformQuotaWindow,
  PlatformQuotaWindowSource,
} from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits(['close', 'success'])

const { t } = useI18n()
const appStore = useAppStore()

const PLATFORMS: PlatformQuotaPlatform[] = ['anthropic', 'openai', 'gemini', 'antigravity', 'grok']

const WINDOWS: PlatformQuotaWindow[] = ['five_hour', 'daily', 'weekly', 'monthly']

interface QuotaRow {
  platform: PlatformQuotaPlatform
  five_hour_limit_usd: number | null
  daily_limit_usd: number | null
  weekly_limit_usd: number | null
  monthly_limit_usd: number | null
  five_hour_usage_usd: number
  daily_usage_usd: number
  weekly_usage_usd: number
  monthly_usage_usd: number
  five_hour_window_source?: PlatformQuotaWindowSource
  weekly_window_source?: PlatformQuotaWindowSource
  five_hour_window_resets_at?: string | null
  weekly_window_resets_at?: string | null
}

const hasActiveSubscription = computed(() =>
  props.user?.subscriptions?.some((s) => s.status === 'active') ?? false
)

const loading = ref(false)
const submitting = ref(false)
const resetting = reactive<Record<string, boolean>>({})
const quotas = ref<QuotaRow[]>([])

function emptyRow(p: PlatformQuotaPlatform): QuotaRow {
  return {
    platform: p,
    five_hour_limit_usd: null,
    daily_limit_usd: null,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    five_hour_usage_usd: 0,
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
  }
}

function normalize(items: PlatformQuotaItem[]): QuotaRow[] {
  const byPlatform = new Map<PlatformQuotaPlatform, PlatformQuotaItem>()
  for (const it of items) byPlatform.set(it.platform, it)
  return PLATFORMS.map((p) => {
    const it = byPlatform.get(p)
    if (!it) return emptyRow(p)
    return {
      platform: p,
      five_hour_limit_usd: it.five_hour_limit_usd ?? null,
      daily_limit_usd: it.daily_limit_usd ?? null,
      weekly_limit_usd: it.weekly_limit_usd ?? null,
      monthly_limit_usd: it.monthly_limit_usd ?? null,
      five_hour_usage_usd: it.five_hour_usage_usd ?? 0,
      daily_usage_usd: it.daily_usage_usd ?? 0,
      weekly_usage_usd: it.weekly_usage_usd ?? 0,
      monthly_usage_usd: it.monthly_usage_usd ?? 0,
      five_hour_window_source: it.five_hour_window_source,
      weekly_window_source: it.weekly_window_source,
      five_hour_window_resets_at: it.five_hour_window_resets_at,
      weekly_window_resets_at: it.weekly_window_resets_at,
    }
  })
}

// 哪些平台的 5h/周 正跟随基准账号 —— 用于顶部提示条。
const syncedPlatforms = computed(() =>
  quotas.value
    .filter((r) => r.five_hour_window_source === 'account' || r.weekly_window_source === 'account')
    .map((r) => r.platform)
)

// 跟随基准账号的档位，刷新时刻由上游决定，悬停给出具体时刻避免困惑。
function windowTooltip(row: QuotaRow, win: 'five_hour' | 'weekly'): string {
  const source = win === 'five_hour' ? row.five_hour_window_source : row.weekly_window_source
  if (source !== 'account') return ''
  const resetsAt = win === 'five_hour' ? row.five_hour_window_resets_at : row.weekly_window_resets_at
  if (!resetsAt) return t('admin.users.platformQuota.syncedBadge')
  return t('admin.users.platformQuota.syncedTooltip', {
    time: new Date(resetsAt).toLocaleString(),
  })
}

function formatUsage(n: number): string {
  if (n == null || Number.isNaN(n)) return '-'
  return n.toFixed(2)
}

async function load() {
  if (!props.user) return
  loading.value = true
  try {
    const data = await adminAPI.users.getPlatformQuotas(props.user.id)
    quotas.value = normalize(data.platform_quotas || [])
  } catch {
    appStore.showError(t('admin.users.platformQuota.loadFailed'))
    quotas.value = PLATFORMS.map(emptyRow)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (s) => { if (s && props.user) load() },
)

function onClearAll() {
  // 二次确认：一键清空全部平台的 daily/weekly/monthly 限额属于高风险批量操作，
  // 误点后所有平台变为"无限额"，且本地无 undo 机制（需要逐个手动重填或取消保存）。
  const confirmed = window.confirm(t('admin.users.platformQuota.clearAllConfirm'))
  if (!confirmed) return
  for (const row of quotas.value) {
    row.five_hour_limit_usd = null
    row.daily_limit_usd = null
    row.weekly_limit_usd = null
    row.monthly_limit_usd = null
  }
}

async function onSave() {
  if (!props.user) return
  // 校验所有 input：v-model.number 在用户输入"0."等中间状态时会写回 NaN，
  // 之前的 normalizeLimit(NaN) 静默返回 null（"无限制"），把"有限额"配置悄悄改成"无限制"。
  // 这里在 save 前显式检测 NaN，提示用户修正后再提交。
  const invalid: string[] = []
  for (const row of quotas.value) {
    for (const win of WINDOWS) {
      const v = row[`${win}_limit_usd` as const]
      if (typeof v === 'number' && Number.isNaN(v)) {
        invalid.push(`${row.platform}.${win}`)
      }
    }
  }
  if (invalid.length > 0) {
    appStore.showError(t('admin.users.platformQuota.invalidNumber', { fields: invalid.join(', ') }))
    return
  }

  submitting.value = true
  try {
    const payload = quotas.value.map((r) => ({
      platform: r.platform,
      five_hour_limit_usd: normalizeLimit(r.five_hour_limit_usd),
      daily_limit_usd: normalizeLimit(r.daily_limit_usd),
      weekly_limit_usd: normalizeLimit(r.weekly_limit_usd),
      monthly_limit_usd: normalizeLimit(r.monthly_limit_usd),
    }))
    await adminAPI.users.updatePlatformQuotas(props.user.id, payload)
    appStore.showSuccess(t('admin.users.platformQuota.updateSuccess'))
    emit('success')
    emit('close')
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.users.platformQuota.updateFailed'))
  } finally {
    submitting.value = false
  }
}

// 仅在合法输入下返回数字：null/undefined/NaN/±Inf/负数 → null（视为"无限额"）。
// 调用方负责在 NaN 路径上做单独的用户提示（见 onSave）。
function normalizeLimit(v: number | null | undefined): number | null {
  if (v === null || v === undefined) return null
  if (typeof v === 'number' && Number.isFinite(v) && v >= 0) return v
  return null
}

// 窗口标识 → i18n 后缀。不能用「首字母大写」推导：five_hour 会拼出 windowFive_hour。
const WINDOW_LABEL_KEY: Record<PlatformQuotaWindow, string> = {
  five_hour: 'windowFiveHour',
  daily: 'windowDaily',
  weekly: 'windowWeekly',
  monthly: 'windowMonthly',
}

async function onReset(platform: PlatformQuotaPlatform, quotaWindow: PlatformQuotaWindow) {
  if (!props.user) return
  const windowLabel = t(`admin.users.platformQuota.${WINDOW_LABEL_KEY[quotaWindow]}`)
  const confirmed = window.confirm(
    t('admin.users.platformQuota.reset.confirm', { platform, window: windowLabel })
  )
  if (!confirmed) return
  const key = `${platform}.${quotaWindow}`
  resetting[key] = true
  try {
    const data = await adminAPI.users.resetPlatformQuotaWindow(props.user.id, platform, quotaWindow)
    quotas.value = normalize(data.platform_quotas || [])
    appStore.showSuccess(t('admin.users.platformQuota.reset.success', { platform, window: windowLabel }))
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.users.platformQuota.reset.failed'))
  } finally {
    resetting[key] = false
  }
}
</script>
