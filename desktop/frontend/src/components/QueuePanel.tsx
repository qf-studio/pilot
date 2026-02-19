import React from 'react'
import { Card } from './ui/Card'
import { StatusIcon } from './ui/StatusIcon'
import { ProgressBar } from './ui/ProgressBar'
import { api } from '../provider'

const { OpenInBrowser } = api
import type { QueueTask } from '../types'

type TaskStatus = 'done' | 'running' | 'queued' | 'pending' | 'failed'

const STATUS_ORDER: Record<string, number> = {
  done: 0,
  running: 1,
  queued: 2,
  pending: 3,
  failed: 4,
}

const STATE_LABELS: Record<string, string> = {
  done: 'done   ',
  running: 'running',
  queued: 'queued ',
  pending: 'pending',
  failed: 'failed ',
}

const STATE_COLORS: Record<string, string> = {
  done: 'text-sage',
  running: 'text-steel',
  queued: 'text-midgray',
  pending: 'text-gray',
  failed: 'text-rose',
}

function metaText(task: QueueTask): string {
  switch (task.status) {
    case 'running':
      return `${Math.round(task.progress * 100)}%`
    case 'done':
      return task.prURL ? `PR` : 'done'
    case 'failed':
      return 'fail'
    case 'queued':
      return 'queue'
    case 'pending':
      return 'wait'
    default:
      return ''
  }
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, max - 1) + '…'
}

interface QueueRowProps {
  task: QueueTask
  shimmerIndex: number
}

function QueueRow({ task, shimmerIndex }: QueueRowProps) {
  const status = task.status as TaskStatus
  const stateLabel = STATE_LABELS[task.status] ?? task.status
  const stateColor = STATE_COLORS[task.status] ?? 'text-midgray'

  function handleClick() {
    const url = task.issueURL || task.prURL
    if (url) OpenInBrowser(url)
  }

  return (
    <div
      className="flex items-center gap-1 px-2 py-0.5 hover:bg-slate/30 cursor-pointer rounded transition-colors"
      onClick={handleClick}
    >
      <StatusIcon status={status} />
      <span className={`text-[10px] w-16 shrink-0 ${stateColor}`}>{stateLabel}</span>
      <span className="text-steel text-[10px] w-14 shrink-0 font-bold">
        {truncate(task.issueID, 10)}
      </span>
      <span className="text-lightgray text-[10px] flex-1 min-w-0 truncate">
        {task.title}
      </span>
      <div className="shrink-0 mx-1">
        <ProgressBar
          status={status}
          progress={task.progress}
          shimmerDelay={shimmerIndex}
        />
      </div>
      <span className="text-gray text-[10px] w-10 text-right shrink-0">
        {metaText(task)}
      </span>
    </div>
  )
}

interface QueuePanelProps {
  tasks: QueueTask[]
}

export function QueuePanel({ tasks }: QueuePanelProps) {
  const sorted = [...tasks].sort((a, b) => {
    const oa = STATUS_ORDER[a.status] ?? 99
    const ob = STATUS_ORDER[b.status] ?? 99
    return oa - ob
  })

  // Track shimmer index per queued item for staggered animation
  let queuedIdx = 0

  return (
    <Card title="QUEUE" className="flex-1 min-h-0">
      <div className="overflow-y-auto h-full log-scroll">
        {sorted.length === 0 ? (
          <div className="text-gray text-[10px] px-1 py-1">no active tasks</div>
        ) : (
          sorted.map((task) => {
            const shimmerIndex = task.status === 'queued' ? queuedIdx++ : 0
            return <QueueRow key={task.id} task={task} shimmerIndex={shimmerIndex} />
          })
        )}
      </div>
    </Card>
  )
}
