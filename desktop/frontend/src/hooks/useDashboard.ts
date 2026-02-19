import { useState, useEffect, useRef } from 'react'
import type { DashboardMetrics, QueueTask, HistoryEntry, AutopilotStatus, ServerStatus, LogEntry } from '../types'
import { api } from '../provider'
import { useDashboardLogs } from './useWebSocket'

const { GetMetrics, GetQueueTasks, GetHistory, GetAutopilotStatus, GetServerStatus } = api

export interface DashboardState {
  metrics: DashboardMetrics
  queueTasks: QueueTask[]
  history: HistoryEntry[]
  autopilot: AutopilotStatus
  server: ServerStatus
  logs: LogEntry[]
}

const defaultMetrics: DashboardMetrics = {
  totalTokens: 0,
  inputTokens: 0,
  outputTokens: 0,
  totalCostUSD: 0,
  totalTasks: 0,
  succeededTasks: 0,
  failedTasks: 0,
  tokenSparkline: [0, 0, 0, 0, 0, 0, 0],
  costSparkline: [0, 0, 0, 0, 0, 0, 0],
  queueSparkline: [0, 0, 0, 0, 0, 0, 0],
}

const defaultAutopilot: AutopilotStatus = {
  enabled: false,
  environment: '',
  autoRelease: false,
  activePRs: [],
  failureCount: 0,
}

const defaultServer: ServerStatus = {
  running: false,
}

// useDashboard polls Wails backend bindings at 1s (data) and 5s (server status) intervals.
export function useDashboard(): DashboardState {
  const [metrics, setMetrics] = useState<DashboardMetrics>(defaultMetrics)
  const [queueTasks, setQueueTasks] = useState<QueueTask[]>([])
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [autopilot, setAutopilot] = useState<AutopilotStatus>(defaultAutopilot)
  const [server, setServer] = useState<ServerStatus>(defaultServer)

  // Logs are streamed via WebSocket (falls back to polling in Wails mode).
  const logs = useDashboardLogs()

  const tickRef = useRef(0)

  useEffect(() => {
    async function poll() {
      tickRef.current += 1
      const t = tickRef.current

      // Data: every 1 second
      try {
        const [m, q, h, ap] = await Promise.all([
          GetMetrics(),
          GetQueueTasks(),
          GetHistory(5),
          GetAutopilotStatus(),
        ])
        if (m) setMetrics(m)
        if (q) setQueueTasks(q)
        if (h) setHistory(h)
        if (ap) setAutopilot(ap)
      } catch {
        // Graceful degradation — keep previous values
      }

      // Server status: every 5 seconds
      if (t % 5 === 0) {
        try {
          const s = await GetServerStatus()
          if (s) setServer(s)
        } catch {
          // Ignore
        }
      }
    }

    // Initial load
    poll()

    const id = setInterval(poll, 1000)
    return () => clearInterval(id)
  }, [])

  return { metrics, queueTasks, history, autopilot, server, logs }
}
