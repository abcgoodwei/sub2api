<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.description') }}</p>
        </div>
        <div class="flex items-center gap-3">
          <label class="inline-flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <input v-model="autoRefresh" type="checkbox" class="rounded border-gray-300 text-primary-600" />
            {{ t('admin.harvestFlow.autoRefresh') }}
          </label>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="fetchFlow">
            <Icon name="refresh" size="sm" class="mr-1" :class="{ 'animate-spin': loading || refreshing }" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </div>

      <div v-if="errorMessage" class="rounded-2xl bg-red-50 p-4 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
        {{ errorMessage }}
      </div>

      <div v-if="loading && !snapshot" class="flex items-center justify-center py-16">
        <LoadingSpinner />
      </div>

      <template v-else-if="snapshot">
        <div class="card overflow-hidden p-5">
          <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span class="inline-flex items-center gap-1 rounded-full px-2 py-0.5" :class="snapshot.harvest.enabled ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-gray-100 text-gray-500 dark:bg-dark-700'">
                {{ snapshot.harvest.enabled ? t('admin.harvestFlow.harvestOn') : t('admin.harvestFlow.harvestOff') }}
              </span>
              <span class="inline-flex items-center gap-1 rounded-full px-2 py-0.5" :class="snapshot.harvest.fail_closed ? 'bg-rose-50 text-rose-700 dark:bg-rose-900/30 dark:text-rose-300' : 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'">
                {{ snapshot.harvest.fail_closed ? t('admin.harvestFlow.failClosed') : t('admin.harvestFlow.failOpen') }}
              </span>
              <span>{{ t('admin.harvestFlow.strategy') }} {{ snapshot.harvest.strategy }}</span>
              <span>{{ t('admin.harvestFlow.scope') }} {{ scopeLabel(snapshot.harvest.scope_mode, snapshot.harvest.account_policy, snapshot.harvest.group_ids) }}</span>
              <span>{{ t('admin.harvestFlow.interval') }} {{ t('admin.harvestFlow.seconds', { n: snapshot.harvest.probe_interval_seconds }) }}</span>
              <span>{{ t('admin.harvestFlow.cooldown') }} {{ t('admin.harvestFlow.seconds', { n: snapshot.harvest.cooldown_seconds }) }}</span>
              <span>{{ t('admin.harvestFlow.targetLength', { n: snapshot.harvest.target_length }) }}</span>
              <span v-if="snapshot.harvest.models?.length">{{ t('admin.harvestFlow.models') }} {{ snapshot.harvest.models.join(' / ') }}</span>
            </div>
            <p v-if="lastUpdated" class="text-xs text-gray-400">{{ t('admin.harvestFlow.lastUpdated', { time: lastUpdated }) }}</p>
          </div>

          <ol class="grid grid-cols-1 gap-3 md:grid-cols-5">
            <li v-for="(stage, index) in snapshot.stages" :key="stage.id" class="relative">
              <div class="h-full rounded-2xl border p-4 transition-colors" :class="stageCardClass(stage.status)">
                <div class="mb-3 flex items-center justify-between">
                  <div class="flex h-9 w-9 items-center justify-center rounded-xl" :class="stageIconClass(stage.status)">
                    <Icon :name="stageIcon(stage.id)" size="sm" />
                  </div>
                  <span class="text-[11px] font-medium uppercase tracking-wide" :class="stageTextClass(stage.status)">
                    {{ statusLabel(stage.status) }}
                  </span>
                </div>
                <p class="text-sm font-semibold text-gray-900 dark:text-white">{{ t(`admin.harvestFlow.stages.${stage.id}`) }}</p>
                <p class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-gray-400">{{ stageDetail(stage) }}</p>
                <p v-if="stage.model || stage.at || stage.http_status" class="mt-2 text-[11px] text-gray-400">
                  <span v-if="stage.model">{{ stage.model }}</span>
                  <span v-if="stage.http_status"> · HTTP {{ stage.http_status }}</span>
                  <span v-if="(stage.model || stage.http_status) && stage.at"> · </span>
                  <span v-if="stage.at">{{ formatClock(stage.at) }}</span>
                </p>
              </div>
              <div v-if="index < snapshot.stages.length - 1" class="pointer-events-none absolute right-[-10px] top-1/2 hidden h-px w-5 bg-gradient-to-r from-gray-300 to-transparent md:block dark:from-dark-600" />
            </li>
          </ol>
        </div>

        <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.sidecar') }}</p>
            <p class="mt-1 text-sm font-semibold" :class="snapshot.sidecar.reachable ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'">
              {{ snapshot.sidecar.reachable ? t('admin.harvestFlow.sidecarReachable') : t('admin.harvestFlow.sidecarOffline') }}
            </p>
            <p class="mt-2 break-all font-mono text-xs text-gray-500 dark:text-gray-400">
              {{ snapshot.sidecar.now || (snapshot.sidecar.reachable ? t('admin.harvestFlow.poolOnline', { n: snapshot.sidecar.all_count || 0 }) : t('admin.harvestFlow.waitingSidecar')) }}
            </p>
            <p class="mt-1 text-xs text-gray-400">{{ t('admin.harvestFlow.nodePool') }} {{ snapshot.sidecar.all_count || 0 }} · {{ snapshot.sidecar.group || 'CODEX-ROTATE' }}</p>
            <p v-if="snapshot.sidecar.error" class="mt-1 text-xs text-rose-500">{{ snapshot.sidecar.error }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.ready') }}</p>
            <p class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{{ snapshot.counts.tickets_ready }}</p>
            <p class="text-xs text-rose-500">{{ t('admin.harvestFlow.paused') }} {{ snapshot.counts.tickets_blocked }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.probeHit') }} / {{ t('admin.harvestFlow.probeMiss') }}</p>
            <p class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{{ snapshot.counts.probe_hit }} / {{ snapshot.counts.probe_miss }}</p>
            <p class="text-xs text-gray-400">{{ t('admin.harvestFlow.ticketAccept') }} {{ snapshot.counts.ticket_accept }} · {{ t('admin.harvestFlow.ticketReject') }} {{ snapshot.counts.ticket_reject }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.selectOk') }}</p>
            <p class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{{ snapshot.counts.select_ok }}</p>
            <p class="text-xs text-gray-400">{{ t('admin.harvestFlow.selectSkip') }} {{ snapshot.counts.select_skip }} · {{ t('admin.harvestFlow.selectFail') }} {{ snapshot.counts.select_fail }}</p>
            <p v-if="snapshot.harvest.harvest_proxy" class="mt-2 truncate font-mono text-[11px] text-gray-400">{{ snapshot.harvest.harvest_proxy }}</p>
          </div>
        </div>

        <!-- ⚙️ 自动打票参数配置
             标题条永久可见；折叠开关独占右侧并锚定 min-width，展开/收起时位置与尺寸都不跳动；
             「恢复默认值 / 保存参数」下移到折叠区底部，随面板一起收起。 -->
        <div class="card overflow-hidden border border-emerald-200 dark:border-emerald-800/40 bg-emerald-50/20 dark:bg-emerald-950/10">
          <div
            class="flex flex-wrap items-center justify-between gap-3 px-5 py-3.5"
            :class="autoConfigOpen ? 'border-b border-emerald-100 dark:border-emerald-900/40' : ''"
          >
            <div class="min-w-0">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white flex flex-wrap items-center gap-2">
                <span>⚙️ 自动打票参数配置</span>
                <span class="text-[10px] bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-300 px-2 py-0.5 rounded font-medium">实时保存生效</span>
              </h2>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">直接调整后台自动巡检节拍与并发，保存后下轮打票周期自动对齐。</p>
            </div>
            <button
              type="button"
              class="btn btn-sm shrink-0 justify-center min-w-[168px]"
              :class="autoConfigOpen ? 'btn-secondary' : 'btn-primary bg-emerald-600 hover:bg-emerald-700 text-white'"
              @click="toggleAutoConfigPanel"
            >
              <Icon name="cog" size="sm" class="mr-1" />
              {{ autoConfigOpen ? '收起自动打票配置' : '自定义自动打票配置' }}
            </button>
          </div>

          <div
            class="grid transition-[grid-template-rows] duration-300 ease-out"
            :style="{ gridTemplateRows: autoConfigOpen ? '1fr' : '0fr' }"
          >
            <div class="overflow-hidden">
              <div class="px-5 pt-4 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5">
            <div>
              <label class="block text-xs font-medium text-gray-700 dark:text-gray-300">检测账号票据的周期</label>
              <div class="relative mt-1">
                <input v-model.number="autoConfigForm.probe_interval_seconds" type="number" min="10" max="1800" class="input input-sm w-full font-mono pr-7 text-xs" />
                <span class="absolute right-2 top-2 text-[11px] text-gray-400">秒</span>
              </div>
              <p class="mt-1 text-[11px] text-gray-400">全池巡检等待 (10~1800s)</p>
            </div>
            <div>
              <label class="block text-xs font-medium text-gray-700 dark:text-gray-300">每轮打号并发</label>
              <div class="relative mt-1">
                <input v-model.number="autoConfigForm.max_probes_per_round" type="number" min="1" max="50" class="input input-sm w-full font-mono pr-8 text-xs" />
                <span class="absolute right-2 top-2 text-[11px] text-gray-400">个号</span>
              </div>
              <p class="mt-1 text-[11px] text-gray-400">每轮最大账号数 (1~50)</p>
            </div>
            <div>
              <label class="block text-xs font-medium text-gray-700 dark:text-gray-300">失败冷却</label>
              <div class="relative mt-1">
                <input v-model.number="autoConfigForm.cooldown_seconds" type="number" min="5" max="600" class="input input-sm w-full font-mono pr-7 text-xs" />
                <span class="absolute right-2 top-2 text-[11px] text-gray-400">秒</span>
              </div>
              <p class="mt-1 text-[11px] text-gray-400">未中锁定冷却 (5~600s)</p>
            </div>
            <div>
              <label class="block text-xs font-medium text-gray-700 dark:text-gray-300">单次探针超时</label>
              <div class="relative mt-1">
                <input v-model.number="autoConfigForm.attempt_timeout_seconds" type="number" min="5" max="60" class="input input-sm w-full font-mono pr-7 text-xs" />
                <span class="absolute right-2 top-2 text-[11px] text-gray-400">秒</span>
              </div>
              <p class="mt-1 text-[11px] text-gray-400">握手等待上限 (5~60s)</p>
            </div>
            <div>
              <label class="block text-xs font-medium text-gray-700 dark:text-gray-300">提前补票阈值</label>
              <div class="relative mt-1">
                <input v-model.number="autoConfigForm.refresh_before_seconds" type="number" min="60" max="1800" class="input input-sm w-full font-mono pr-7 text-xs" />
                <span class="absolute right-2 top-2 text-[11px] text-gray-400">秒</span>
              </div>
              <p class="mt-1 text-[11px] text-gray-400">到期前提前秒数 (默认10m)</p>
            </div>
              </div>

              <!-- 操作按钮置于卡片底部，随折叠区一起收起 -->
              <div class="mx-5 mb-5 mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-dashed border-emerald-200/70 pt-3 dark:border-emerald-900/40">
                <span class="text-[11px] text-gray-400">提示：保存后写入配置数据库，后台打票线程下一轮自动对齐新参数。</span>
                <div class="ml-auto flex items-center gap-2">
                  <button type="button" class="btn btn-secondary btn-sm" @click="resetAutoConfig">恢复默认值</button>
                  <button
                    type="button"
                    class="btn btn-primary btn-sm bg-emerald-600 hover:bg-emerald-700 text-white"
                    :disabled="savingAutoConfig"
                    @click="saveAutoConfig"
                  >
                    {{ savingAutoConfig ? '保存中...' : '💾 保存参数' }}
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 🎯 手动打票专属控制台卡片 -->
        <div class="card overflow-hidden border border-gray-200 dark:border-dark-700 bg-white dark:bg-dark-800 rounded-xl shadow-sm">
          <div class="px-4 py-3 bg-gray-50/50 dark:bg-dark-700/50 border-b border-gray-100 dark:border-dark-700 flex justify-between items-center">
            <div class="flex items-center gap-2">
              <span class="font-semibold text-gray-900 dark:text-white text-sm">🎯 手动打票</span>
              <span class="text-xs bg-emerald-50 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300 px-2 py-0.5 rounded border border-emerald-200 dark:border-emerald-800">单号直通</span>
              <span class="text-xs text-gray-400">绕开全局排队，自由调频、单号定向打票</span>
            </div>
            <span class="text-xs text-gray-400 font-mono">出口: {{ snapshot.sidecar.group || 'CODEX-ROTATE' }}</span>
          </div>

          <div class="grid grid-cols-1 lg:grid-cols-12">
            <!-- 左侧参数设置区 (5 列) -->
            <div class="lg:col-span-5 p-4 border-r border-gray-100 dark:border-dark-700 flex flex-col gap-3">
              <div>
                <label class="text-xs font-semibold text-gray-700 dark:text-gray-300 flex justify-between">
                  <span>目标账号</span>
                  <span class="text-gray-400 font-normal">支持按 #ID 或名称搜索</span>
                </label>
                <div class="relative mt-1">
                  <input
                    ref="manualAccountInputRef"
                    v-model="manualAccountKeyword"
                    type="text"
                    placeholder="点击查看全部账号，或输入 #ID / 名称搜索"
                    class="input input-sm w-full text-xs"
                    autocomplete="off"
                    @focus="openManualAccountDropdown"
                    @input="openManualAccountDropdown"
                  />
                </div>

                <!-- 下拉必须 Teleport 到 body：外层卡片是 overflow-hidden，absolute 会被裁掉。
                     定位用 fixed + getBoundingClientRect，滚动/缩放时跟随重算。 -->
                <Teleport to="body">
                  <div
                    v-if="manualAccountDropdownOpen"
                    ref="manualAccountDropdownRef"
                    class="fixed z-[9999] overflow-hidden rounded-lg border border-gray-200 bg-white shadow-xl dark:border-dark-700 dark:bg-dark-800"
                    :style="manualAccountDropdownStyle"
                  >
                    <div class="flex items-center justify-between gap-2 border-b border-gray-100 bg-gray-50 px-3 py-1.5 text-[11px] text-gray-500 dark:border-dark-700 dark:bg-dark-700/50 dark:text-gray-400">
                      <span>共 {{ orderedManualAccounts.length }} 个账号</span>
                      <span><span class="font-medium text-emerald-600 dark:text-emerald-400">{{ manualAccountSortLabel }}</span> · 最多 10 条</span>
                    </div>
                    <div class="max-h-[490px] overflow-y-auto">
                      <button
                        v-for="acc in orderedManualAccounts"
                        :key="acc.id"
                        type="button"
                        class="flex h-[49px] w-full items-center gap-2.5 border-b border-gray-50 px-3 text-left last:border-b-0 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-700"
                        @click="selectManualAccount(acc)"
                      >
                        <span class="shrink-0 rounded border border-emerald-200 bg-emerald-50 px-1.5 py-0.5 font-mono text-[11px] font-semibold text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300">
                          #{{ acc.id }}
                        </span>
                        <span class="min-w-0 flex-1">
                          <span class="block truncate text-xs text-gray-900 dark:text-white">{{ acc.name || '（无名称）' }}</span>
                          <span class="block truncate text-[10px] text-gray-400">{{ manualAccountMeta(acc) }}</span>
                        </span>
                        <span class="flex shrink-0 flex-col items-end gap-0.5">
                          <span class="whitespace-nowrap rounded-full border px-2 py-0.5 text-[10px] font-semibold" :class="availabilityClass(acc)">
                            {{ availabilityLabel(acc) }}
                          </span>
                          <span v-if="availabilityRecoverText(acc)" class="font-mono text-[10px] text-gray-400">{{ availabilityRecoverText(acc) }}</span>
                        </span>
                      </button>
                      <div v-if="!orderedManualAccounts.length" class="px-3 py-6 text-center text-xs text-gray-400">暂无可选账号</div>
                    </div>
                  </div>
                </Teleport>
              </div>

              <div>
                <label class="text-xs font-semibold text-gray-700 dark:text-gray-300">打票模型</label>
                <div class="flex gap-2 mt-1">
                  <label v-for="m in availableManualModels" :key="m" class="flex-1 border dark:border-dark-700 rounded px-2 py-1.5 text-xs flex items-center justify-center gap-1.5 cursor-pointer" :class="manualSelectedModels.includes(m) ? 'border-emerald-300 bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 font-medium' : 'text-gray-600 dark:text-gray-400'">
                    <input type="checkbox" :value="m" v-model="manualSelectedModels" class="accent-emerald-600" />
                    <span>{{ m }}</span>
                  </label>
                </div>
              </div>

              <div class="grid grid-cols-2 gap-2">
                <div>
                  <label class="text-xs font-semibold text-gray-700 dark:text-gray-300">重试间隔 (1~300s)</label>
                  <div class="relative mt-1">
                    <input v-model.number="manualForm.probe_interval_seconds" type="number" min="1" max="300" class="input input-sm w-full pr-7 text-xs font-mono" />
                    <span class="absolute right-2 top-2 text-[11px] text-gray-400">秒</span>
                  </div>
                  <p class="mt-0.5 text-[10px] text-gray-400">对应自动打票检测周期</p>
                </div>
                <div>
                  <label class="text-xs font-semibold text-gray-700 dark:text-gray-300">429 冷静 (1~60s)</label>
                  <div class="relative mt-1">
                    <input v-model.number="manualForm.rate_limit_cooldown_seconds" type="number" min="1" max="60" class="input input-sm w-full pr-7 text-xs font-mono" />
                    <span class="absolute right-2 top-2 text-[11px] text-gray-400">秒</span>
                  </div>
                  <p class="mt-0.5 text-[10px] text-gray-400">对应自动打票失败冷却</p>
                </div>
              </div>

              <div class="grid grid-cols-2 gap-2">
                <div>
                  <label class="text-xs font-semibold text-gray-700 dark:text-gray-300">最大尝试</label>
                  <input v-model.number="manualForm.max_attempts" type="number" min="1" max="100" class="input input-sm w-full mt-1 text-xs font-mono" />
                </div>
                <div>
                  <label class="text-xs font-semibold text-gray-700 dark:text-gray-300">换节点规则</label>
                  <select v-model="manualForm.node_switch_rule" class="input input-sm w-full mt-1 text-xs">
                    <option value="312_or_2fail">命中 312 或连败2次切</option>
                    <option value="every_request">每发必切</option>
                    <option value="312_only">仅 312 切</option>
                    <option value="never">固定当前出口</option>
                  </select>
                </div>
              </div>

              <div class="flex items-center gap-2 mt-1">
                <input type="checkbox" id="stopOnSuccess" v-model="manualForm.stop_on_success" class="accent-emerald-600" />
                <label for="stopOnSuccess" class="text-xs text-gray-600 dark:text-gray-300 select-none cursor-pointer">出票即停（成功获取合规门票并入库后自动终止）</label>
              </div>

              <div class="flex gap-2 pt-2 border-t border-dashed dark:border-dark-700">
                <button v-if="!manualHarvesting" type="button" class="btn btn-primary btn-sm flex-1 bg-emerald-600 hover:bg-emerald-700 text-white" :disabled="!selectedManualAccount" @click="startManualHarvest">
                  ▶ 开始打票
                </button>
                <button v-else type="button" class="btn btn-danger btn-sm flex-1 bg-rose-600 hover:bg-rose-700 text-white" @click="stopManualHarvest">
                  ⏹ 停止打票
                </button>
                <button type="button" class="btn btn-secondary btn-sm" @click="manualLogs = []">清空日志</button>
              </div>
            </div>

            <!-- 右侧实时监控与流式日志 (7 列) -->
            <div class="lg:col-span-7 flex flex-col bg-gray-50/30 dark:bg-dark-900/30">
              <div class="px-4 py-2 border-b border-gray-100 dark:border-dark-700 flex justify-between text-xs bg-white dark:bg-dark-800">
                <div><span class="text-gray-400">状态:</span> <strong :class="manualStatusColor">{{ manualStatusText }}</strong></div>
                <div><span class="text-gray-400">尝试进度:</span> <span class="font-mono font-semibold">{{ manualProgressText }}</span></div>
                <div><span class="text-gray-400">当前节点:</span> <span class="font-mono text-primary-600">{{ manualCurrentNode || '-' }}</span></div>
                <div><span class="text-gray-400">入库门票:</span> <strong class="text-emerald-600 font-mono">{{ manualTicketsStoredCount }} 张</strong></div>
              </div>

              <div class="p-3 font-mono text-xs overflow-y-auto max-h-[420px] flex flex-col gap-1.5 text-gray-700 dark:text-gray-300">
                <div v-for="(log, idx) in manualLogs" :key="idx" class="flex items-start gap-2">
                  <span class="shrink-0 pt-0.5 text-[10px] text-gray-400">{{ log.time }}</span>
                  <span class="shrink-0 rounded px-1 py-0.5 text-[10px] font-semibold uppercase" :class="logTagClass(log.level)">{{ log.level }}</span>
                  <div class="min-w-0 flex-1">
                    <div class="break-all leading-snug">{{ log.message }}</div>
                    <div v-if="log.detail" class="mt-0.5 break-all text-[10px] text-gray-400">{{ log.detail }}</div>
                  </div>
                </div>
                <div v-if="!manualLogs.length" class="text-gray-400 italic text-center py-8">点击【开始打票】发起单号定向探针...</div>
              </div>
            </div>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-6 xl:grid-cols-5">
          <div class="card p-5 xl:col-span-2">
            <h2 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.accounts') }}</h2>
            <div v-if="!snapshot.accounts?.length" class="text-sm text-gray-500">{{ t('admin.harvestFlow.noAccounts') }}</div>
            <div v-else class="space-y-3">
              <div v-for="account in snapshot.accounts" :key="account.id" class="rounded-2xl border border-gray-100 p-4 dark:border-dark-700">
                <div class="mb-3 flex items-center justify-between gap-2">
                  <div>
                    <p class="font-medium text-gray-900 dark:text-white">{{ account.name || `#${account.id}` }}</p>
                    <p class="text-xs text-gray-400">
                      {{ account.status }} · {{ account.schedulable ? t('admin.harvestFlow.schedulable') : t('admin.harvestFlow.unschedulable') }}
                      <span v-if="account.skip_harvest"> · {{ t('admin.harvestFlow.skipHarvestBadge') }}</span>
                      <span v-else-if="account.in_scope"> · {{ t('admin.harvestFlow.harvestAccountBadge') }}</span>
                    </p>
                    <p v-if="account.skip_harvest" class="mt-1 text-[11px] text-amber-600 dark:text-amber-400">{{ t('admin.harvestFlow.skipHarvestHint') }}</p>
                  </div>
                  <div class="flex items-center gap-2">
                    <button
                      type="button"
                      class="btn btn-secondary btn-sm"
                      :disabled="!!skipSaving[account.id]"
                      @click="toggleSkipHarvest(account)"
                    >
                      {{ account.skip_harvest ? t('admin.harvestFlow.enableHarvest') : t('admin.harvestFlow.skipHarvest') }}
                    </button>
                    <span class="text-xs text-gray-400">{{ account.ready_count }}/{{ account.tickets?.length ?? 0 }}</span>
                  </div>
                </div>
                <div class="flex flex-wrap gap-2">
                  <span
                    v-for="ticket in account.tickets"
                    :key="ticket.model"
                    class="rounded-2xl px-2.5 py-1.5 text-xs"
                    :class="ticket.ready ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : ticket.blocked ? 'bg-rose-50 text-rose-700 dark:bg-rose-900/30 dark:text-rose-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'"
                  >
                    <span class="font-medium">{{ ticket.model }}</span>
                    <span v-if="ticket.ready && account.skip_harvest"> · {{ t('admin.harvestFlow.leftoverUnused') }}</span>
                    <span v-else-if="ticket.ready"> · {{ t('admin.harvestFlow.remaining', { time: formatRemaining(ticket.remaining_seconds) }) }}</span>
                    <span v-else-if="ticket.blocked"> · {{ t('admin.harvestFlow.blocked') }}</span>
                    <span v-else> · {{ t('admin.harvestFlow.missing') }}</span>
                    <span v-if="ticket.length" class="block font-mono text-[11px] opacity-80">{{ ticket.length }}B</span>
                    <span v-if="ticket.probe?.result" class="block text-[11px] opacity-80">
                      {{ resultLabel(ticket.probe.result) }}
                      <span v-if="ticket.probe.http_status"> · HTTP {{ ticket.probe.http_status }}</span>
                    </span>
                  </span>
                </div>
              </div>
            </div>
          </div>

          <div class="card p-5 xl:col-span-3">
            <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.events') }}</h2>
              <div class="flex flex-wrap gap-1">
                <button
                  v-for="filter in eventFilters"
                  :key="filter"
                  type="button"
                  class="rounded-full px-2.5 py-1 text-[11px]"
                  :class="eventFilter === filter ? 'bg-primary-600 text-white' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'"
                  @click="eventFilter = filter"
                >
                  {{ filter === 'all' ? t('admin.harvestFlow.filterAll') : t(`admin.harvestFlow.stages.${filter}`) }}
                </button>
              </div>
            </div>
            <div v-if="!snapshot.events?.length" class="text-sm text-gray-500">{{ t('admin.harvestFlow.noEvents') }}</div>
            <ol v-else class="max-h-[560px] space-y-2 overflow-auto pr-1">
              <li
                v-for="event in filteredEvents"
                :key="event.id"
                class="flex gap-3 rounded-xl border border-gray-100 p-3 dark:border-dark-700"
              >
                <span class="mt-1 h-2.5 w-2.5 flex-shrink-0 rounded-full" :class="eventDotClass(event.kind)" />
                <div class="min-w-0 flex-1">
                  <div class="flex flex-wrap items-center justify-between gap-2">
                    <p class="text-sm font-medium text-gray-900 dark:text-white">
                      {{ kindLabel(event.kind) }}
                      <span v-if="event.model" class="ml-1 font-mono text-xs text-gray-500">{{ event.model }}</span>
                    </p>
                    <span class="text-[11px] text-gray-400">{{ formatClock(event.at) }}</span>
                  </div>
                  <p class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-gray-400">
                    <span v-if="event.account_name">{{ event.account_name }}</span>
                    <span v-if="event.node"> · {{ event.node }}</span>
                    <span v-if="event.length"> · {{ event.length }}/{{ event.blocks || '-' }}</span>
                    <span v-if="event.expected_length && event.length && event.length !== event.expected_length">
                      · {{ t('admin.harvestFlow.wantShape', { length: event.expected_length, blocks: event.expected_blocks || '-' }) }}
                    </span>
                    <span v-if="event.http_status"> · HTTP {{ event.http_status }}</span>
                    <span v-if="event.standby"> · {{ t('admin.harvestFlow.standby') }}</span>
                  </p>
                  <p v-if="event.reason || event.detail || event.result" class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ reasonLabel(event.reason) || resultLabel(event.result) || event.detail || event.result }}
                  </p>
                </div>
              </li>
            </ol>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import { getCodexHarvestFlow, updateCodexSkipHarvest, updateCodexHarvestConfig, type CodexHarvestFlowAccount, type CodexHarvestFlowEvent, type CodexHarvestFlowSnapshot, type CodexHarvestFlowStage } from '@/api/admin/accounts'
import { buildApiUrl } from '@/api/client'
import { useAppStore } from '@/stores'

const { t } = useI18n()
const appStore = useAppStore()
const snapshot = ref<CodexHarvestFlowSnapshot | null>(null)
const loading = ref(false)
const refreshing = ref(false)
const autoRefresh = ref(true)
const errorMessage = ref('')
const lastUpdated = ref('')
const eventFilter = ref<'all' | 'node' | 'probe' | 'shape' | 'ticket' | 'select'>('all')
const eventFilters = ['all', 'node', 'probe', 'shape', 'ticket', 'select'] as const
const skipSaving = ref<Record<number, boolean>>({})
let timer: number | undefined

// ⚙️ 自动打票参数配置状态
const autoConfigOpen = ref(false)
const savingAutoConfig = ref(false)
const autoConfigForm = ref({
  probe_interval_seconds: 180,
  max_probes_per_round: 6,
  cooldown_seconds: 180,
  attempt_timeout_seconds: 25,
  refresh_before_seconds: 600,
})

// 这几个键的服务端默认值（config.go / viper.SetDefault），仅在快照缺字段时兜底。
const AUTO_CONFIG_FALLBACK = {
  probe_interval_seconds: 180,
  max_probes_per_round: 6,
  cooldown_seconds: 180,
  attempt_timeout_seconds: 25,
  refresh_before_seconds: 600,
}

function toggleAutoConfigPanel() {
  autoConfigOpen.value = !autoConfigOpen.value
  if (!autoConfigOpen.value) return
  const harvest = snapshot.value?.harvest
  if (!harvest) return
  // 全部 5 项都从快照回读。此前 attempt_timeout_seconds / refresh_before_seconds
  // 不在快照里，只能硬编码 25/600，表现为"保存成功但重开面板永远是默认值"。
  autoConfigForm.value = {
    probe_interval_seconds: harvest.probe_interval_seconds ?? AUTO_CONFIG_FALLBACK.probe_interval_seconds,
    max_probes_per_round: harvest.max_probes_per_round ?? AUTO_CONFIG_FALLBACK.max_probes_per_round,
    cooldown_seconds: harvest.cooldown_seconds ?? AUTO_CONFIG_FALLBACK.cooldown_seconds,
    attempt_timeout_seconds: harvest.attempt_timeout_seconds ?? AUTO_CONFIG_FALLBACK.attempt_timeout_seconds,
    refresh_before_seconds: harvest.refresh_before_seconds ?? AUTO_CONFIG_FALLBACK.refresh_before_seconds,
  }
}

async function saveAutoConfig() {
  if (savingAutoConfig.value) return
  savingAutoConfig.value = true
  errorMessage.value = ''
  const form = { ...autoConfigForm.value }
  try {
    await updateCodexHarvestConfig(form)
    // 成功提示：站点自带的 Toast 组件（右上角、绿色左边条、4 秒自动消失）。
    appStore.showSuccess(
      `修改成功 · 周期 ${form.probe_interval_seconds}s / 并发 ${form.max_probes_per_round} / ` +
        `冷却 ${form.cooldown_seconds}s / 超时 ${form.attempt_timeout_seconds}s / 提前 ${form.refresh_before_seconds}s`,
      4000,
    )
    // 不再 await fetchFlow()：该接口实测 4~50s，等它会把"保存中"拖到几十秒。
    // 面板保持展开，既有的自动轮询会去刷新快照。
    void fetchFlow()
  } catch (err: any) {
    const status = err?.response?.status
    const serverMessage = err?.response?.data?.message || err?.message
    errorMessage.value = status
      ? `保存失败（HTTP ${status}）：${serverMessage || '未知错误'}`
      : `保存失败：${serverMessage || '未知错误'}`
  } finally {
    savingAutoConfig.value = false
  }
}

function resetAutoConfig() {
  autoConfigForm.value = { ...AUTO_CONFIG_FALLBACK }
}

// 🎯 手动打票状态与交互
const manualAccountKeyword = ref('')
const manualAccountDropdownOpen = ref(false)
const selectedManualAccount = ref<CodexHarvestFlowAccount | null>(null)
const availableManualModels = computed(() => snapshot.value?.harvest?.models || ['gpt-6-astra', 'gpt-5.6-sol'])
const manualSelectedModels = ref<string[]>(['gpt-6-astra', 'gpt-5.6-sol'])
const manualHarvesting = ref(false)
// 当前手动打票请求的取消句柄；停止按钮与组件卸载都用它真正中断后端任务。
let manualAbortController: AbortController | null = null
const manualStatusText = ref('待命中')
const manualStatusColor = ref('text-gray-400')
const manualProgressText = ref('0 / 20')
const manualCurrentNode = ref('')
const manualTicketsStoredCount = ref(0)
/** 日志条目：level 决定颜色与严重程度，message 是通俗主文案，detail 是原始技术细节。 */
const manualLogs = ref<Array<{ time: string; level: string; message: string; detail?: string }>>([])

// ── 账号可用性口径：与后端 ResolveCodexAccountAvailability、调度 SQL 一致 ──
// 注意 schedulable 只代表"账号未被停用"，不等于"有额度/没被限流"，
// 因此状态必须由后端算好的 availability 决定（老后端缺失时再粗略兜底）。
type AvailabilityMeta = { label: string; cls: string; rank: number }

const AVAILABILITY_META: Record<string, AvailabilityMeta> = {
  available: {
    label: '🟢 可用',
    cls: 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300',
    rank: 0,
  },
  rate_limited: {
    label: '🟡 限流中',
    cls: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-300',
    rank: 1,
  },
  overload: {
    label: '🟠 过载中',
    cls: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-300',
    rank: 2,
  },
  temp_unschedulable: {
    label: '🟠 临时停用',
    cls: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-300',
    rank: 3,
  },
  error: {
    label: '🔴 凭证失效',
    cls: 'border-rose-200 bg-rose-50 text-rose-700 dark:border-rose-800 dark:bg-rose-950/40 dark:text-rose-300',
    rank: 4,
  },
  disabled: {
    label: '⚪ 已停用',
    cls: 'border-gray-200 bg-gray-100 text-gray-500 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-400',
    rank: 5,
  },
  expired: {
    label: '⚪ 已过期',
    cls: 'border-gray-200 bg-gray-100 text-gray-500 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-400',
    rank: 6,
  },
}

function accountAvailability(acc: CodexHarvestFlowAccount): string {
  if (acc.availability && AVAILABILITY_META[acc.availability]) return acc.availability
  if (acc.status && acc.status !== 'active') return 'error'
  if (!acc.schedulable) return 'disabled'
  return 'available'
}

function availabilityMeta(acc: CodexHarvestFlowAccount): AvailabilityMeta {
  return AVAILABILITY_META[accountAvailability(acc)] ?? AVAILABILITY_META.disabled
}

function availabilityLabel(acc: CodexHarvestFlowAccount): string {
  return availabilityMeta(acc).label
}

function availabilityClass(acc: CodexHarvestFlowAccount): string {
  return availabilityMeta(acc).cls
}

/** 恢复时间文案：当天只显示时分，跨天补上月日。 */
function availabilityRecoverText(acc: CodexHarvestFlowAccount): string {
  if (!acc.recover_at) return ''
  const target = new Date(acc.recover_at)
  if (Number.isNaN(target.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  const clock = `${pad(target.getHours())}:${pad(target.getMinutes())}`
  if (target.toDateString() === new Date().toDateString()) return `${clock} 恢复`
  return `${pad(target.getMonth() + 1)}-${pad(target.getDate())} ${clock} 恢复`
}

const AVAILABILITY_REASON: Record<string, string> = {
  rate_limited: '上游 429 限流',
  overload: '上游过载保护',
  temp_unschedulable: '临时不可调度窗口',
  error: '需重新登录',
  disabled: '已被停用调度',
  expired: '账号已到期',
}

function manualAccountMeta(acc: CodexHarvestFlowAccount): string {
  const tickets = `${acc.ready_count}/${acc.tickets?.length ?? 0} 张票`
  const reason = AVAILABILITY_REASON[accountAvailability(acc)]
  return reason ? `${tickets} · ${reason}` : tickets
}

/** 账号显示标签，必须与选中后写回输入框的内容完全一致（排序逻辑依赖它）。 */
function accountLabel(acc: CodexHarvestFlowAccount): string {
  return `#${acc.id} · ${acc.name || 'Account'}`
}

/** 相似度打分：ID 精确 1000 > 标签前缀 900 > 名称前缀 800 > 包含 600/500 > 子序列 300。 */
function accountMatchScore(acc: CodexHarvestFlowAccount, query: string): number {
  if (!query) return 0
  const idText = `#${acc.id}`
  const name = (acc.name || '').toLowerCase()
  const label = accountLabel(acc).toLowerCase()
  if (idText === query || String(acc.id) === query) return 1000
  if (label.startsWith(query)) return 900
  if (name && name.startsWith(query)) return 800
  if (label.includes(query)) return 600
  if (name && name.includes(query)) return 500
  let matched = 0
  for (const ch of label) {
    if (ch === query[matched]) matched += 1
    if (matched >= query.length) break
  }
  return matched >= query.length ? 300 : 0
}

/**
 * 下拉列表：永远展示【全部】账号，只是顺序在变，绝不因输入而清空。
 *  - 空输入（含"输入框里正好是已选账号标签"的情况）→ 按可用性排序，可用的排最上
 *  - 有输入 → 按相似度排序，同分再按可用性排
 */
const orderedManualAccounts = computed(() => {
  const list = snapshot.value?.accounts || []
  const raw = manualAccountKeyword.value.trim()
  const selected = selectedManualAccount.value
  // 选中账号后输入框里是完整标签，此时必须视为"关键词为空"，
  // 否则列表会被过滤成只剩它自己 —— 这正是之前"看不到其他账号"的原因。
  const query = selected && raw === accountLabel(selected) ? '' : raw.toLowerCase()
  return [...list].sort((a, b) => {
    if (query) {
      const score = accountMatchScore(b, query) - accountMatchScore(a, query)
      if (score !== 0) return score
    }
    const rank = availabilityMeta(a).rank - availabilityMeta(b).rank
    if (rank !== 0) return rank
    const aAt = a.recover_at ? Date.parse(a.recover_at) : 0
    const bAt = b.recover_at ? Date.parse(b.recover_at) : 0
    if (aAt !== bAt) return aAt - bAt
    return a.id - b.id
  })
})

const manualAccountSortLabel = computed(() => {
  const raw = manualAccountKeyword.value.trim()
  const selected = selectedManualAccount.value
  if (selected && raw === accountLabel(selected)) return '按可用性排序'
  return raw ? '按相似度排序' : '按可用性排序'
})

const manualAccountInputRef = ref<HTMLInputElement | null>(null)
const manualAccountDropdownRef = ref<HTMLElement | null>(null)
const manualAccountDropdownStyle = ref<Record<string, string>>({})

const DROPDOWN_HEADER_HEIGHT = 30
const DROPDOWN_ROW_HEIGHT = 49
const DROPDOWN_MAX_ROWS = 10
const DROPDOWN_MIN_SPACE = 240

/**
 * 定位策略：优先向下展开，空间不够就压缩列表高度（"最多 10 条"是上限而非定值）。
 * 只有下方不足 240px 且上方明显更宽裕时才向上翻转，避免输入框位于页面中下部时
 * 列表频繁往上盖住上方内容。
 */
function positionManualAccountDropdown() {
  const input = manualAccountInputRef.value
  const dropdown = manualAccountDropdownRef.value
  if (!input || !dropdown) return
  const rect = input.getBoundingClientRect()
  const gap = 4
  const below = window.innerHeight - rect.bottom - gap - 8
  const above = rect.top - gap - 8
  const openDown = below >= DROPDOWN_MIN_SPACE || below >= above
  const available = (openDown ? below : above) - DROPDOWN_HEADER_HEIGHT
  const maxList = DROPDOWN_MAX_ROWS * DROPDOWN_ROW_HEIGHT
  const listHeight = Math.max(DROPDOWN_ROW_HEIGHT * 2, Math.min(maxList, available))
  const style: Record<string, string> = {
    left: `${rect.left}px`,
    width: `${rect.width}px`,
    maxHeight: `${listHeight + DROPDOWN_HEADER_HEIGHT}px`,
  }
  if (openDown) {
    style.top = `${rect.bottom + gap}px`
  } else {
    style.bottom = `${window.innerHeight - rect.top + gap}px`
  }
  manualAccountDropdownStyle.value = style
}

function openManualAccountDropdown() {
  manualAccountDropdownOpen.value = true
  void nextTick(positionManualAccountDropdown)
}

function closeManualAccountDropdown() {
  manualAccountDropdownOpen.value = false
}

function handleManualAccountOutsideClick(event: MouseEvent) {
  if (!manualAccountDropdownOpen.value) return
  const target = event.target as Node | null
  if (!target) return
  if (manualAccountInputRef.value?.contains(target)) return
  if (manualAccountDropdownRef.value?.contains(target)) return
  closeManualAccountDropdown()
}

function handleManualAccountViewportChange() {
  if (manualAccountDropdownOpen.value) positionManualAccountDropdown()
}

function selectManualAccount(acc: CodexHarvestFlowAccount) {
  selectedManualAccount.value = acc
  manualAccountKeyword.value = accountLabel(acc)
  manualAccountDropdownOpen.value = false
  addManualLog('SELECT', `已选定目标账号: #${acc.id} ${acc.name || ''}`.trim())
}

const manualForm = ref({
  probe_interval_seconds: 10,
  rate_limit_cooldown_seconds: 30,
  max_attempts: 20,
  node_switch_rule: '312_or_2fail',
  stop_on_success: true,
})

function addManualLog(level: string, message: string, detail?: string) {
  const time = new Date().toTimeString().split(' ')[0]
  manualLogs.value.unshift({ time, level: level.toUpperCase(), message, detail })
  if (manualLogs.value.length > 200) manualLogs.value.pop()
}

/** 老版本后端不返回 level 时，按 result 推导一个级别。 */
function levelForResult(result?: string): string {
  switch (result) {
    case 'hit':
      return 'OK'
    case 'rate_limited':
    case 'miss_degraded':
      return 'WARN'
    case 'error':
      return 'ERROR'
    default:
      return 'INFO'
  }
}

function logTagClass(level: string) {
  switch (level) {
    case 'OK':
      return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
    case 'WARN':
      return 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300'
    case 'ERROR':
      return 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'
    case 'SELECT':
      return 'bg-violet-100 text-violet-800 dark:bg-violet-950/60 dark:text-violet-300'
    case 'START':
      return 'bg-sky-100 text-sky-800 dark:bg-sky-950/60 dark:text-sky-300'
    case 'STOP':
      return 'bg-gray-200 text-gray-700 dark:bg-dark-600 dark:text-gray-300'
    default:
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
  }
}

async function startManualHarvest() {
  if (!selectedManualAccount.value) return
  manualHarvesting.value = true
  manualStatusText.value = '打票中...'
  manualStatusColor.value = 'text-amber-500'
  manualProgressText.value = `0 / ${manualForm.value.max_attempts}`
  manualTicketsStoredCount.value = 0
  addManualLog('START', `向账号 #${selectedManualAccount.value.id} 发起定向打票...`)

  // 取消信号要贯穿整个流：仅仅改本地状态不会停掉后端循环，请求会一直跑到
  // max_attempts。abort 后 fetch 抛错，后端 ctx 也随之取消并停止打上游。
  manualAbortController?.abort()
  const controller = new AbortController()
  manualAbortController = controller

  try {
    // 原生 fetch 不经过 apiClient 拦截器，必须手动带上鉴权头，
    // 否则 admin 路由会直接返回 401 UNAUTHORIZED。
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream',
      'X-Admin-UI-Request': '1',
    }
    const token = localStorage.getItem('auth_token')
    if (token) {
      headers.Authorization = `Bearer ${token}`
    }

    const res = await fetch(buildApiUrl(`/admin/accounts/${selectedManualAccount.value.id}/manual-harvest`), {
      method: 'POST',
      credentials: 'include',
      headers,
      signal: controller.signal,
      body: JSON.stringify({
        models: manualSelectedModels.value,
        probe_interval_seconds: manualForm.value.probe_interval_seconds,
        rate_limit_cooldown_seconds: manualForm.value.rate_limit_cooldown_seconds,
        max_attempts: manualForm.value.max_attempts,
        node_switch_rule: manualForm.value.node_switch_rule,
        stop_on_success: manualForm.value.stop_on_success,
      })
    })

    if (res.status === 401) {
      addManualLog('ERROR', '登录状态已失效（HTTP 401），请刷新页面重新登录后再试。')
      manualStatusText.value = '鉴权失败'
      manualStatusColor.value = 'text-rose-500'
      return
    }

    if (!res.ok || !res.body) {
      let detail = ''
      try {
        const body = await res.text()
        detail = body ? ` - ${body.slice(0, 200)}` : ''
      } catch (_) {
        // 忽略：读取错误响应体失败不影响后续错误上报
      }
      throw new Error(`HTTP ${res.status}${detail}`)
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''

    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n\n')
      buffer = lines.pop() || ''

      for (const line of lines) {
        if (line.startsWith('data: ')) {
          try {
            const data = JSON.parse(line.slice(6))
            manualProgressText.value = `${data.attempt} / ${data.max_attempts}`
            if (data.node) manualCurrentNode.value = data.node
            if (data.tickets_stored) manualTicketsStoredCount.value = data.tickets_stored

            // 后端已给出 level（OK/WARN/ERROR）与 detail（原始技术错误）；
            // 老版本没有这两个字段时按 result 推导，保持向后兼容。
            addManualLog(
              (data.level as string) || levelForResult(data.result),
              data.message || '',
              (data.detail as string) || '',
            )

            if (data.done) {
              manualHarvesting.value = false
              manualStatusText.value = data.tickets_stored > 0 ? '打票成功' : '任务结束'
              manualStatusColor.value = data.tickets_stored > 0 ? 'text-emerald-500' : 'text-gray-400'
              fetchFlow()
            }
          } catch (_) {
            // 忽略：单条 SSE 帧解析失败不应打断整个打票流
          }
        }
      }
    }
  } catch (err: any) {
    // 主动终止不是异常：stopManualHarvest 已经把状态与日志写好了。
    if (!controller.signal.aborted) {
      addManualLog('ERROR', `连接断开或打票中断: ${err.message}`)
    }
  } finally {
    if (manualAbortController === controller) {
      manualAbortController = null
    }
    manualHarvesting.value = false
  }
}

function stopManualHarvest() {
  manualAbortController?.abort()
  manualAbortController = null
  manualHarvesting.value = false
  manualStatusText.value = '已停止'
  manualStatusColor.value = 'text-gray-400'
  addManualLog('STOP', '管理员手动终止了打票任务。')
}

const filteredEvents = computed(() => {
  const events = snapshot.value?.events || []
  if (eventFilter.value === 'all') return events
  if (eventFilter.value === 'shape') {
    return events.filter((event: CodexHarvestFlowEvent) => event.stage === 'probe' || event.kind === 'accept' || event.kind === 'reject')
  }
  return events.filter((event: CodexHarvestFlowEvent) => event.stage === eventFilter.value)
})

const stageIcons: Record<string, 'globe' | 'bolt' | 'beaker' | 'key' | 'user'> = {
  node: 'globe',
  probe: 'bolt',
  shape: 'beaker',
  ticket: 'key',
  select: 'user'
}

function stageIcon(id: string) {
  return stageIcons[id] || 'bolt'
}

function stageCardClass(status: string) {
  switch (status) {
    case 'ok':
      return 'border-emerald-200 bg-emerald-50/60 dark:border-emerald-900/40 dark:bg-emerald-950/20'
    case 'warn':
      return 'border-amber-200 bg-amber-50/70 dark:border-amber-900/40 dark:bg-amber-950/20'
    case 'fail':
      return 'border-rose-200 bg-rose-50/70 dark:border-rose-900/40 dark:bg-rose-950/20'
    default:
      return 'border-gray-100 bg-gray-50/80 dark:border-dark-700 dark:bg-dark-800/40'
  }
}

function stageIconClass(status: string) {
  switch (status) {
    case 'ok':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
    case 'warn':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
    case 'fail':
      return 'bg-rose-100 text-rose-700 dark:bg-rose-900/40 dark:text-rose-300'
    default:
      return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-300'
  }
}

function stageTextClass(status: string) {
  switch (status) {
    case 'ok':
      return 'text-emerald-600 dark:text-emerald-400'
    case 'warn':
      return 'text-amber-600 dark:text-amber-400'
    case 'fail':
      return 'text-rose-600 dark:text-rose-400'
    default:
      return 'text-gray-400'
  }
}

function eventDotClass(kind: string) {
  if (kind === 'probe_hit' || kind === 'accept' || kind === 'selected' || kind === 'rotate') {
    return 'bg-emerald-500'
  }
  if (kind === 'skip' || kind === 'probe_miss') {
    return 'bg-amber-500'
  }
  return 'bg-rose-500'
}

function lookupLabel(prefix: string, value?: string) {
  if (!value) return ''
  const key = `${prefix}.${value}`
  const label = t(key)
  return label === key ? value : label
}

function statusLabel(status: string) {
  return lookupLabel('admin.harvestFlow.status', status)
}

function kindLabel(kind: string) {
  return lookupLabel('admin.harvestFlow.kinds', kind)
}

function reasonLabel(reason?: string) {
  return lookupLabel('admin.harvestFlow.reasons', reason)
}

function resultLabel(result?: string) {
  return lookupLabel('admin.harvestFlow.results', result)
}

function scopeLabel(mode?: string, policy?: string, groupIds?: number[]) {
  const scope = mode === 'selected'
    ? t('admin.harvestFlow.scopeSelected', { n: groupIds?.length || 0 })
    : t('admin.harvestFlow.scopeAll')
  const accountPolicy = policy === 'prioritize_schedulable'
    ? t('admin.harvestFlow.policyPrioritize')
    : t('admin.harvestFlow.policySchedulable')
  return `${scope} · ${accountPolicy}`
}

function stageDetail(stage: CodexHarvestFlowStage) {
  switch (stage.id) {
    case 'node':
      if (snapshot.value?.sidecar.now) return snapshot.value.sidecar.now
      if (snapshot.value?.sidecar.reachable) {
        return t('admin.harvestFlow.poolOnline', { n: snapshot.value.sidecar.all_count || 0 })
      }
      return t('admin.harvestFlow.waitingSidecar')
    case 'probe':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleProbe')
      return resultLabel(stage.detail) || stage.node || stage.detail || t('admin.harvestFlow.idleProbe')
    case 'shape':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleShape')
      if (stage.status === 'ok') {
        return t('admin.harvestFlow.shapeOk', { length: stage.length || 0, blocks: stage.blocks || 0 })
      }
      if (stage.status === 'warn' || stage.detail === 'no ticket body') {
        return t('admin.harvestFlow.shapeNoBody')
      }
      return t('admin.harvestFlow.shapeBad', {
        length: stage.length || 0,
        blocks: stage.blocks || 0,
        expected_length: stage.expected_length || 292,
        expected_blocks: stage.expected_blocks || 10
      })
    case 'ticket':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleTicket')
      if (stage.status === 'fail') {
        return snapshot.value?.counts.tickets_blocked
          ? t('admin.harvestFlow.ticketsPausedCount', { n: snapshot.value.counts.tickets_blocked })
          : t('admin.harvestFlow.ticketRejected')
      }
      if (stage.detail === 'standby stored') return t('admin.harvestFlow.ticketStandby')
      return t('admin.harvestFlow.ticketsReadyCount', { n: snapshot.value?.counts.tickets_ready ?? 0 })
    case 'select':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleSelect')
      return reasonLabel(stage.detail) || resultLabel(stage.detail) || stage.detail || t('admin.harvestFlow.idleSelect')
    default:
      return stage.detail || '—'
  }
}

function formatClock(value?: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleTimeString()
}

function formatRemaining(seconds: number) {
  const total = Math.max(0, seconds)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m`
  return `${total}s`
}

async function fetchFlow() {
  if (snapshot.value) {
    refreshing.value = true
  } else {
    loading.value = true
  }
  errorMessage.value = ''
  try {
    snapshot.value = await getCodexHarvestFlow()
    lastUpdated.value = new Date().toLocaleTimeString()
  } catch (error) {
    const err = error as { message?: string }
    errorMessage.value = err?.message || String(error)
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

async function toggleSkipHarvest(account: CodexHarvestFlowAccount) {
  skipSaving.value = { ...skipSaving.value, [account.id]: true }
  errorMessage.value = ''
  try {
    await updateCodexSkipHarvest(account.id, !account.skip_harvest)
    await fetchFlow()
  } catch (error) {
    const err = error as { message?: string }
    errorMessage.value = err?.message || String(error)
  } finally {
    skipSaving.value = { ...skipSaving.value, [account.id]: false }
  }
}

onMounted(() => {
  void fetchFlow()
  timer = window.setInterval(() => {
    if (autoRefresh.value) void fetchFlow()
  }, 5000)
})

onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
})

// 下拉是 fixed 定位且挂在 body 下，需要自己跟随滚动/缩放，并在点击外部时关闭。
onMounted(() => {
  window.addEventListener('scroll', handleManualAccountViewportChange, true)
  window.addEventListener('resize', handleManualAccountViewportChange)
  document.addEventListener('mousedown', handleManualAccountOutsideClick)
})

onBeforeUnmount(() => {
  window.removeEventListener('scroll', handleManualAccountViewportChange, true)
  window.removeEventListener('resize', handleManualAccountViewportChange)
  document.removeEventListener('mousedown', handleManualAccountOutsideClick)
  // 离开页面时不要让后端的打票循环继续跑。
  manualAbortController?.abort()
  manualAbortController = null
})
</script>
