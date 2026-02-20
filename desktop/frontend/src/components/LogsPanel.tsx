import React, { useRef, useEffect } from 'react'
import { Card } from './ui/Card'
import type { LogEntry } from '../types'

export type { LogEntry }

const LEVEL_COLORS: Record<string, string> = {
  info: 'text-lightgray',
  warn: 'text-amber',
  error: 'text-rose',
}

/** Extract [GH-XXXX] or [PROJ-NNN] style task ID from component or message */
function extractTaskID(entry: LogEntry): string | null {
  // Check component field first
  if (entry.component) {
    const match = entry.component.match(/^[A-Z]+-\d+$/)
    if (match) return match[0]
  }
  // Check message for [GH-XXXX] prefix
  const msgMatch = entry.message.match(/^\[([A-Z]+-\d+)\]/)
  if (msgMatch) return msgMatch[1]
  return null
}

/** Strip leading [GH-XXXX] from message if we display it separately */
function stripTaskPrefix(message: string): string {
  return message.replace(/^\[[A-Z]+-\d+\]\s*/, '')
}

interface LogsPanelProps {
  entries: LogEntry[]
}

export function LogsPanel({ entries }: LogsPanelProps) {
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [entries])

  return (
    <Card title="LOGS" className="flex-1 min-h-0">
      <div ref={scrollRef} className="overflow-y-auto h-full log-scroll">
        {entries.length === 0 ? (
          <div className="text-gray text-[10px]">no log entries</div>
        ) : (
          entries.map((e, i) => {
            const taskID = extractTaskID(e)
            const message = taskID ? stripTaskPrefix(e.message) : e.message
            return (
              <div key={i} className="flex gap-1.5 text-[10px] leading-tight py-px">
                <span className="text-gray shrink-0">{e.ts}</span>
                {taskID && (
                  <span className="shrink-0 font-bold" style={{ color: '#7eb8da' }}>
                    [{taskID}]
                  </span>
                )}
                <span className={LEVEL_COLORS[e.level ?? 'info'] ?? 'text-lightgray'}>
                  {message}
                </span>
              </div>
            )
          })
        )}
      </div>
    </Card>
  )
}
