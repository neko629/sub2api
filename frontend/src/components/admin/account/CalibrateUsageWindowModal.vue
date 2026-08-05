<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.calibrateWindow.title')"
    width="normal"
    @close="$emit('close')"
  >
    <div v-if="account" class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.accounts.calibrateWindow.subtitle', { name: account.name }) }}
      </p>

      <!-- 校准写的是本地跟踪状态：能查上游的账号会被下次探测覆盖，必须讲清楚 -->
      <div
        class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs leading-relaxed text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      >
        {{
          account.type === 'oauth'
            ? t('admin.accounts.calibrateWindow.oauthNotice')
            : t('admin.accounts.calibrateWindow.setupTokenNotice')
        }}
      </div>

      <div class="space-y-1">
        <label class="block text-xs font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.accounts.calibrateWindow.windowLabel') }}
        </label>
        <select v-model="form.window" class="input w-full">
          <option value="5h">{{ t('admin.accounts.calibrateWindow.window5h') }}</option>
          <option value="7d">{{ t('admin.accounts.calibrateWindow.window7d') }}</option>
          <option value="7d_oi">{{ t('admin.accounts.calibrateWindow.window7dOi') }}</option>
        </select>
      </div>

      <div class="space-y-1">
        <label class="block text-xs font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.accounts.calibrateWindow.resetsAtLabel') }}
        </label>
        <input v-model="form.resetsAt" type="datetime-local" class="input w-full" />
        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.calibrateWindow.resetsAtHint', { hours: maxHours }) }}
        </p>
      </div>

      <label class="flex cursor-pointer items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
        <input v-model="form.resetUsage" type="checkbox" />
        <span>{{ t('admin.accounts.calibrateWindow.resetUsage') }}</span>
      </label>

      <div
        v-if="error"
        class="rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-600 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300"
      >
        {{ error }}
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">
          {{ t('common.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="submitting" @click="onSubmit">
          {{ submitting ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { Account } from '@/types'
import type { CalibratableUsageWindow } from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits(['close', 'success'])

const { t } = useI18n()
const appStore = useAppStore()

// 与后端 calibrationSpan 保持一致：超出即视为误输入（例如年份填错）。
const MAX_HOURS: Record<CalibratableUsageWindow, number> = { '5h': 6, '7d': 192, '7d_oi': 192 }

const submitting = ref(false)
const error = ref('')
const form = reactive<{
  window: CalibratableUsageWindow
  resetsAt: string
  resetUsage: boolean
}>({ window: '5h', resetsAt: '', resetUsage: true })

const maxHours = computed(() => MAX_HOURS[form.window])

/** Date → `datetime-local` 需要的本地时间串（不能直接用 toISOString，那是 UTC）。 */
function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 打开时预填一个合理默认值：当前时刻 + 该窗口时长的一半，管理员按需微调。 */
function prefill() {
  const hours = form.window === '5h' ? 2.5 : 84
  form.resetsAt = toLocalInput(new Date(Date.now() + hours * 3600_000))
  error.value = ''
}

watch(() => props.show, (visible) => { if (visible) prefill() })
watch(() => form.window, () => { if (props.show) prefill() })

async function onSubmit() {
  if (!props.account) return
  error.value = ''

  const target = new Date(form.resetsAt)
  if (Number.isNaN(target.getTime())) {
    error.value = t('admin.accounts.calibrateWindow.errors.invalidTime')
    return
  }
  const now = Date.now()
  if (target.getTime() <= now) {
    error.value = t('admin.accounts.calibrateWindow.errors.mustBeFuture')
    return
  }
  if (target.getTime() > now + maxHours.value * 3600_000) {
    error.value = t('admin.accounts.calibrateWindow.errors.outOfRange', { hours: maxHours.value })
    return
  }

  // 该账号可能是某平台的「用量基准账号」——前端拿不到这个配置，
  // 所以对 5h / 7d 一律二次确认，宁可多问一次也不要静默清空全体用户的额度。
  if (form.window !== '7d_oi') {
    const confirmed = window.confirm(
      t('admin.accounts.calibrateWindow.referenceConfirm', { time: target.toLocaleString() })
    )
    if (!confirmed) return
  }

  submitting.value = true
  try {
    await adminAPI.accounts.calibrateUsageWindow(props.account.id, {
      window: form.window,
      resets_at: target.toISOString(),
      reset_usage: form.resetUsage,
    })
    appStore.showSuccess(t('admin.accounts.calibrateWindow.success'))
    emit('success')
    emit('close')
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.accounts.calibrateWindow.failed'))
  } finally {
    submitting.value = false
  }
}
</script>
