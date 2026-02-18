import React from 'react'

type Status = 'done' | 'running' | 'queued' | 'pending' | 'failed'

interface StatusIconProps {
  status: Status
  className?: string
}

const ICON_MAP: Record<Status, string> = {
  done: '✓',
  running: '●',
  queued: '◌',
  pending: '·',
  failed: '✗',
}

const COLOR_MAP: Record<Status, string> = {
  done: 'text-sage',
  running: 'text-steel',
  queued: 'text-midgray',
  pending: 'text-gray',
  failed: 'text-rose',
}

export function StatusIcon({ status, className = '' }: StatusIconProps) {
  const icon = ICON_MAP[status] ?? '?'
  const color = COLOR_MAP[status] ?? 'text-midgray'
  const pulse = status === 'running' ? 'pulse' : ''

  return (
    <span className={`inline-block w-4 text-center ${color} ${pulse} ${className}`}>
      {icon}
    </span>
  )
}
