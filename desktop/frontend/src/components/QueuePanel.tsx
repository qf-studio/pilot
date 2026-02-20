import React from 'react'
import { Card } from './ui/Card'
import { StatusIcon } from './ui/StatusIcon'
import { ProgressBar } from './ui/ProgressBar'
import { api } from '../provider'

const { OpenInBrowser } = api
import type { QueueTask } from '../types'

type TaskStatus = 'done' | 'running' | 'queued' | 'pending' | 'failed'

const STATUS_ORDER: Record<string, number> = {
  running: 0,
  queued: 1,
  pending: 2,
  failed: 3,
  done: 4,
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
      return 'done'
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
  const stateColor = STATE_COLORS[task.status] ?? 'text-midgray'

  function handleClick() {
    const url = task.prURL || task.issueURL
    if (url) OpenInBrowser(url)
  }

  return (
    <div
      className="flex items-center gap-3 px-2 py-px hover:bg-slate/30 cursor-pointer rounded transition-colors"
      onClick={handleClick}
    >
      <StatusIcon status={status} />
      <span className="text-steel text-[10px] shrink-0 font-bold whitespace-nowrap">
        {truncate(task.issueID, 8)}
      </span>
      <span className="text-lightgray text-[10px] flex-1 min-w-0 truncate">
        {task.title}
      </span>
      <div className="shrink-0">
        <ProgressBar
          status={status}
          progress={task.progress}
          shimmerDelay={shimmerIndex}
          className="w-20"
        />
      </div>
      <span className={`text-[10px] w-8 text-right shrink-0 ${stateColor}`}>
        {metaText(task)}
      </span>
    </div>
  )
}

interface QueuePanelProps {
  tasks: QueueTask[]
}

// Show running/queued/pending/failed first, then last N done items
const MAX_DONE_VISIBLE = 8

export function QueuePanel({ tasks }: QueuePanelProps) {
  const sorted = [...tasks].sort((a, b) => {
    const oa = STATUS_ORDER[a.status] ?? 99
    const ob = STATUS_ORDER[b.status] ?? 99
    if (oa !== ob) return oa - ob
    // Within same status, newest first
    return b.id.localeCompare(a.id)
  })

  // Split active vs done
  const active = sorted.filter(t => t.status !== 'done')
  const done = sorted.filter(t => t.status === 'done').slice(0, MAX_DONE_VISIBLE)
  const visible = [...active, ...done]
  const hiddenCount = sorted.length - visible.length

  let queuedIdx = 0

  return (
    <Card title={`QUEUE  ${sorted.length}`} className="flex-1 min-h-0">
      <div className="overflow-y-auto h-full log-scroll">
        {visible.length === 0 ? (
          <div className="text-gray text-[10px] px-1 py-1">no active tasks</div>
        ) : (
          <>
            {visible.map((task) => {
              const shimmerIndex = task.status === 'queued' ? queuedIdx++ : 0
              return <QueueRow key={task.id} task={task} shimmerIndex={shimmerIndex} />
            })}
            {hiddenCount > 0 && (
              <div className="text-gray text-[10px] px-1.5 py-1 text-center">
                + {hiddenCount} more completed
              </div>
            )}
          </>
        )}
      </div>
    </Card>
  )
}
