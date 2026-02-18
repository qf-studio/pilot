import React from 'react'
import { Sparkline } from './ui/Sparkline'
import type { DashboardMetrics } from '../types'

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function formatCost(usd: number): string {
  if (usd >= 1000) return `$${usd.toFixed(0)}`
  if (usd >= 1) return `$${usd.toFixed(2)}`
  return `$${usd.toFixed(3)}`
}

interface MetricCardProps {
  title: string
  value: string
  detail1: string
  detail2: string
  sparklineData: number[]
  sparklineColor: string
}

function MetricCard({ title, value, detail1, detail2, sparklineData, sparklineColor }: MetricCardProps) {
  return (
    <div className="flex-1 border border-border rounded bg-card flex flex-col min-w-0">
      <div className="flex items-center justify-between px-2 py-1 border-b border-border">
        <span className="text-midgray uppercase tracking-wider text-[10px]">{title}</span>
        <span className="text-lightgray font-bold text-xs">{value}</span>
      </div>
      <div className="px-2 py-1 flex-1 flex flex-col justify-between">
        <div className="space-y-0.5">
          <div className="text-gray text-[10px]">{detail1}</div>
          <div className="text-gray text-[10px]">{detail2}</div>
        </div>
        <div className="mt-1">
          <Sparkline data={sparklineData} color={sparklineColor} width={100} height={20} />
        </div>
      </div>
    </div>
  )
}

interface MetricsCardsProps {
  metrics: DashboardMetrics
}

export function MetricsCards({ metrics }: MetricsCardsProps) {
  const costPerTask =
    metrics.totalTasks > 0
      ? formatCost(metrics.totalCostUSD / metrics.totalTasks)
      : '$0.000'

  return (
    <div className="flex gap-2 px-2 py-2">
      <MetricCard
        title="TOKENS"
        value={formatTokens(metrics.totalTokens)}
        detail1={`↑ ${formatTokens(metrics.inputTokens)} input`}
        detail2={`↓ ${formatTokens(metrics.outputTokens)} output`}
        sparklineData={metrics.tokenSparkline}
        sparklineColor="#7eb8da"
      />
      <MetricCard
        title="COST"
        value={formatCost(metrics.totalCostUSD)}
        detail1={`${costPerTask}/task`}
        detail2={`${metrics.totalTasks} total tasks`}
        sparklineData={metrics.costSparkline}
        sparklineColor="#7ec699"
      />
      <MetricCard
        title="QUEUE"
        value={String(metrics.totalTasks)}
        detail1={`✓ ${metrics.succeededTasks} done`}
        detail2={`✗ ${metrics.failedTasks} failed`}
        sparklineData={metrics.queueSparkline}
        sparklineColor="#8b949e"
      />
    </div>
  )
}
