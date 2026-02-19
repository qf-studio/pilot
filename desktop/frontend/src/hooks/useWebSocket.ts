import { useState, useEffect, useRef, useCallback } from 'react'
import type { LogEntry } from '../types'
import { api } from '../provider'

const { GetLogs } = api

/** Maximum number of log entries kept in state to bound memory. */
const MAX_LOG_ENTRIES = 200

/** Initial reconnect delay in ms — doubles on each failure (exponential backoff). */
const INITIAL_RECONNECT_MS = 1000

/** Maximum reconnect delay in ms. */
const MAX_RECONNECT_MS = 30000

/** Polling interval (ms) used as fallback when WebSocket is unavailable. */
const POLL_INTERVAL_MS = 2000

/**
 * Resolves the WebSocket URL for the dashboard log stream endpoint.
 * Uses the current page host so it works behind reverse proxies.
 */
function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws/dashboard`
}

/**
 * useDashboardLogs connects to the gateway WebSocket for real-time log streaming.
 * Falls back to polling /api/v1/logs when WebSocket is unavailable (e.g. Wails mode).
 */
export function useDashboardLogs(): LogEntry[] {
  const [logs, setLogs] = useState<LogEntry[]>([])
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectDelay = useRef(INITIAL_RECONNECT_MS)
  const reconnectTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const unmounted = useRef(false)
  const usingFallback = useRef(false)

  const appendLogs = useCallback((newEntries: LogEntry[]) => {
    setLogs((prev) => {
      const merged = [...prev, ...newEntries]
      return merged.length > MAX_LOG_ENTRIES ? merged.slice(-MAX_LOG_ENTRIES) : merged
    })
  }, [])

  useEffect(() => {
    unmounted.current = false

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const isWails = typeof window !== 'undefined' && !!(window as any).go?.main?.App
    if (isWails) {
      // Wails mode: WebSocket not available, use polling.
      usingFallback.current = true
      const id = setInterval(async () => {
        try {
          const l = await GetLogs(20)
          if (l && !unmounted.current) setLogs(l)
        } catch {
          // Graceful degradation
        }
      }, POLL_INTERVAL_MS)
      return () => {
        unmounted.current = true
        clearInterval(id)
      }
    }

    // Browser mode: use WebSocket with auto-reconnect.
    function connect() {
      if (unmounted.current) return

      const ws = new WebSocket(wsURL())
      wsRef.current = ws

      ws.onopen = () => {
        reconnectDelay.current = INITIAL_RECONNECT_MS
        usingFallback.current = false
      }

      ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data)
          if (Array.isArray(data)) {
            // Initial batch
            setLogs(data as LogEntry[])
          } else {
            // Single streamed entry
            appendLogs([data as LogEntry])
          }
        } catch {
          // Ignore malformed messages
        }
      }

      ws.onclose = () => {
        wsRef.current = null
        if (unmounted.current) return
        scheduleReconnect()
      }

      ws.onerror = () => {
        // onclose will fire after onerror — reconnect handled there.
        ws.close()
      }
    }

    function scheduleReconnect() {
      if (unmounted.current) return

      // Start polling fallback while disconnected.
      if (!usingFallback.current) {
        usingFallback.current = true
        startFallbackPolling()
      }

      const delay = reconnectDelay.current
      reconnectDelay.current = Math.min(delay * 2, MAX_RECONNECT_MS)
      reconnectTimer.current = setTimeout(connect, delay)
    }

    let fallbackInterval: ReturnType<typeof setInterval> | null = null

    function startFallbackPolling() {
      if (fallbackInterval) return
      fallbackInterval = setInterval(async () => {
        if (!usingFallback.current) {
          if (fallbackInterval) {
            clearInterval(fallbackInterval)
            fallbackInterval = null
          }
          return
        }
        try {
          const l = await GetLogs(20)
          if (l && !unmounted.current) setLogs(l)
        } catch {
          // Graceful degradation
        }
      }, POLL_INTERVAL_MS)
    }

    connect()

    return () => {
      unmounted.current = true
      if (reconnectTimer.current) clearTimeout(reconnectTimer.current)
      if (fallbackInterval) clearInterval(fallbackInterval)
      if (wsRef.current) {
        wsRef.current.onclose = null
        wsRef.current.close()
        wsRef.current = null
      }
    }
  }, [appendLogs])

  return logs
}
