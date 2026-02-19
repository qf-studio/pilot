import React from 'react'

type BarStatus = 'done' | 'running' | 'queued' | 'pending' | 'failed'

interface ProgressBarProps {
  status: BarStatus
  progress: number // 0.0 - 1.0
  shimmerDelay?: number // 0-4
  className?: string
}

export function ProgressBar({ status, progress, shimmerDelay = 0, className = '' }: ProgressBarProps) {
  const w = className || 'w-20'

  if (status === 'queued') {
    const delayClass = `shimmer-delay-${Math.min(shimmerDelay, 4)}`
    return (
      <div className={`inline-block ${w} h-2 rounded-sm shimmer-bar ${delayClass}`} />
    )
  }

  if (status === 'pending') {
    return (
      <div className={`inline-flex ${w} h-2 rounded-sm bg-slate overflow-hidden`} />
    )
  }

  const pct = Math.max(0, Math.min(1, progress)) * 100
  const fillColor =
    status === 'done'
      ? 'bg-sage'
      : status === 'failed'
      ? 'bg-rose'
      : 'bg-steel'

  return (
    <div className={`inline-flex ${w} h-2 rounded-sm bg-slate overflow-hidden`}>
      <div className={`h-full ${fillColor}`} style={{ width: `${pct}%` }} />
    </div>
  )
}
