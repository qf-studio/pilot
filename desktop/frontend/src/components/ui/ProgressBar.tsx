import React from 'react'

type BarStatus = 'done' | 'running' | 'queued' | 'pending' | 'failed'

interface ProgressBarProps {
  status: BarStatus
  progress: number // 0.0 - 1.0
  shimmerDelay?: number // 0-4
  className?: string
}

export function ProgressBar({ status, progress, shimmerDelay = 0, className = '' }: ProgressBarProps) {
  const filled = Math.round(Math.max(0, Math.min(1, progress)) * 14)
  const empty = 14 - filled

  if (status === 'queued') {
    const delayClass = `shimmer-delay-${Math.min(shimmerDelay, 4)}`
    return (
      <div className={`inline-block w-28 h-3 rounded-sm shimmer-bar ${delayClass} ${className}`} />
    )
  }

  if (status === 'pending') {
    return (
      <div className={`inline-flex w-28 h-3 rounded-sm bg-slate overflow-hidden ${className}`} />
    )
  }

  const fillColor =
    status === 'done'
      ? 'bg-sage'
      : status === 'failed'
      ? 'bg-rose'
      : 'bg-steel'

  return (
    <div className={`inline-flex w-28 h-3 rounded-sm bg-slate overflow-hidden ${className}`}>
      {filled > 0 && (
        <div
          className={`h-full ${fillColor}`}
          style={{ width: `${(filled / 14) * 100}%` }}
        />
      )}
      {empty > 0 && (
        <div
          className="h-full bg-slate"
          style={{ width: `${(empty / 14) * 100}%` }}
        />
      )}
    </div>
  )
}
