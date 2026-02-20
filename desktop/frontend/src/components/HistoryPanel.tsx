import React from 'react'
import { Card } from './ui/Card'
import { api } from '../provider'

const { OpenInBrowser } = api
import type { HistoryEntry } from '../types'

function timeAgo(dateStr: string): string {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return ''
  const seconds = Math.floor((Date.now() - d.getTime()) / 1000)
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

interface HistoryRowProps {
  entry: HistoryEntry
  isSubIssue?: boolean
}

function HistoryRow({ entry, isSubIssue = false }: HistoryRowProps) {
  const isSuccess = entry.status === 'completed'
  const prefix = isSuccess ? '+' : 'x'
  const prefixColor = isSuccess ? 'text-sage' : 'text-rose'
  const indent = isSubIssue ? 'pl-3' : ''

  function handleClick() {
    const url = entry.prURL || ''
    if (url) OpenInBrowser(url)
  }

  return (
    <div
      className={`flex items-center gap-3 px-2 py-px hover:bg-slate/30 cursor-pointer rounded transition-colors ${indent}`}
      onClick={handleClick}
    >
      <span className={`text-[10px] font-bold ${prefixColor} shrink-0`}>{prefix}</span>
      <span className="font-bold text-[10px] shrink-0 whitespace-nowrap" style={{ color: '#7eb8da' }}>{entry.issueID}</span>
      <span className="text-lightgray text-[10px] flex-1 min-w-0 truncate">{entry.title}</span>
      <span className="text-midgray text-[10px] shrink-0">{timeAgo(entry.completedAt)}</span>
    </div>
  )
}

interface EpicGroupProps {
  entry: HistoryEntry
}

function EpicGroup({ entry }: EpicGroupProps) {
  const subIssues = entry.subIssues ?? []
  const total = subIssues.length
  const done = subIssues.filter((s) => s.status === 'completed').length
  const allDone = done === total && total > 0
  const prefix = allDone ? '+' : '~'
  const prefixColor = allDone ? 'text-sage' : 'text-steel'

  return (
    <div className="space-y-0">
      <div className="flex items-center gap-3 px-2 py-px">
        <span className={`text-[10px] font-bold ${prefixColor} shrink-0`}>{prefix}</span>
        <span className="font-bold text-[10px] shrink-0 whitespace-nowrap" style={{ color: '#7eb8da' }}>{entry.issueID}</span>
        <span className="text-lightgray text-[10px] flex-1 min-w-0 truncate">{entry.title}</span>
        <span className="text-midgray text-[10px] shrink-0">[{done}/{total}]</span>
      </div>
      {!allDone && subIssues.map((sub) => (
        <HistoryRow key={sub.id} entry={sub} isSubIssue />
      ))}
    </div>
  )
}

interface HistoryPanelProps {
  entries: HistoryEntry[]
}

export function HistoryPanel({ entries }: HistoryPanelProps) {
  return (
    <Card title="HISTORY" className="shrink-0">
      <div className="overflow-y-auto h-full log-scroll">
        {entries.length === 0 ? (
          <div className="text-gray text-[10px]">no completed tasks</div>
        ) : (
          entries.map((entry) =>
            entry.subIssues && entry.subIssues.length > 0 ? (
              <EpicGroup key={entry.id} entry={entry} />
            ) : (
              <HistoryRow key={entry.id} entry={entry} />
            ),
          )
        )}
      </div>
    </Card>
  )
}
