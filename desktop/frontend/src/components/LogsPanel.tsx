import React, { useRef, useEffect } from 'react'
import { Card } from './ui/Card'
import type { LogEntry } from '../types'

export type { LogEntry }

const LEVEL_COLORS: Record<string, string> = {
  info: 'text-midgray',
  warn: 'text-amber',
  error: 'text-rose',
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
          entries.map((e, i) => (
            <div key={i} className="flex gap-2 text-[10px] leading-tight py-px">
              <span className="text-gray shrink-0">{e.ts}</span>
              <span className={LEVEL_COLORS[e.level ?? 'info'] ?? 'text-midgray'}>
                {e.message}
              </span>
            </div>
          ))
        )}
      </div>
    </Card>
  )
}
