import React, { useRef, useEffect } from 'react'
import { Card } from './ui/Card'

export interface LogEntry {
  ts: string
  message: string
  level?: 'info' | 'warn' | 'error'
}

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
    <Card title="LOGS">
      <div ref={scrollRef} className="log-scroll max-h-24 space-y-0.5">
        {entries.length === 0 ? (
          <div className="text-gray text-[10px]">no log entries</div>
        ) : (
          entries.map((e, i) => (
            <div key={i} className="flex gap-2 text-[10px]">
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
