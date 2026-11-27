import React, { useEffect, useState } from 'react'
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

const videoBase = '/dashboard/videos/'
const executionIdPattern = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/

interface HistoryRowProps {
  entry: HistoryEntry
  isSubIssue?: boolean
  hasVideo: boolean
}

function HistoryRow({ entry, isSubIssue = false, hasVideo }: HistoryRowProps) {
  const [videoOpen, setVideoOpen] = useState(false)
  const videoURL = hasVideo ? `${videoBase}${entry.id}.mp4` : undefined
  const isSuccess = entry.status === 'completed'
  const prefix = isSuccess ? '+' : 'x'
  const prefixColor = isSuccess ? 'text-sage' : 'text-rose'
  const indent = isSubIssue ? 'pl-3' : ''

  function handleClick() {
    const url = entry.prURL || ''
    if (url) OpenInBrowser(url)
  }

  return (
    <div>
      <div className={`flex items-center gap-3 px-2 py-px rounded ${indent}`}>
        <button
          type="button"
          disabled={!entry.prURL}
          className="flex min-w-0 flex-1 items-center gap-3 text-left rounded enabled:cursor-pointer enabled:hover:bg-slate/30"
          onClick={handleClick}
        >
          <span className={`text-[10px] font-bold ${prefixColor} shrink-0`}>{prefix}</span>
          <span className="font-bold text-[10px] shrink-0 whitespace-nowrap" style={{ color: '#7eb8da' }}>{entry.issueID}</span>
          <span className="text-lightgray text-[10px] flex-1 min-w-0 truncate">{entry.title}</span>
        </button>
        <span className="text-midgray text-[10px] shrink-0">{timeAgo(entry.completedAt)}</span>
        {videoURL && (
          <button type="button" aria-expanded={videoOpen} onClick={() => setVideoOpen(open => !open)} className="text-sage text-[10px] shrink-0 underline">
            {videoOpen ? 'Hide video' : 'Play video'}
          </button>
        )}
      </div>
      {videoOpen && videoURL && (
        <div className="px-2 py-1">
          <video controls preload="metadata" playsInline src={videoURL} className="w-full max-h-64" aria-label={`Browser retest for ${entry.issueID}`} />
          <a className="text-sage text-[10px] underline" href={videoURL}>Open video directly</a>
        </div>
      )}
    </div>
  )
}

interface EpicGroupProps {
  entry: HistoryEntry
  videos: ReadonlySet<string>
}

function EpicGroup({ entry, videos }: EpicGroupProps) {
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
      {subIssues.filter(sub => !allDone || videos.has(sub.id)).map(sub => (
        <HistoryRow key={sub.id} entry={sub} isSubIssue hasVideo={videos.has(sub.id)} />
      ))}
    </div>
  )
}

interface HistoryPanelProps {
  entries: HistoryEntry[]
}

export function HistoryPanel({ entries }: HistoryPanelProps) {
  const [videos, setVideos] = useState<ReadonlySet<string>>(() => new Set())

  useEffect(() => {
    if (!window.location.pathname.startsWith('/dashboard/')) return
    const abort = new AbortController()
    fetch(`${videoBase}manifest.json`, { signal: abort.signal })
      .then(response => response.ok ? response.json() : [])
      .then((ids: unknown) => {
        if (Array.isArray(ids) && !abort.signal.aborted) {
          setVideos(new Set(ids.filter((id): id is string => typeof id === 'string' && executionIdPattern.test(id))))
        }
      })
      .catch(() => { /* Artifact playback is optional. */ })
    return () => abort.abort()
  }, [])

  return (
    <Card title="HISTORY" className="shrink-0">
      <div className="overflow-y-auto h-full log-scroll">
        {entries.length === 0 ? (
          <div className="text-gray text-[10px]">no completed tasks</div>
        ) : (
          entries.map((entry) =>
            entry.subIssues && entry.subIssues.length > 0 ? (
              <EpicGroup key={entry.id} entry={entry} videos={videos} />
            ) : (
              <HistoryRow key={entry.id} entry={entry} hasVideo={videos.has(entry.id)} />
            ),
          )
        )}
      </div>
    </Card>
  )
}
