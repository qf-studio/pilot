// HTTP-based data provider for browser mode.
// Same function signatures as wailsjs.ts, but uses fetch() against gateway API endpoints.

import type { DashboardMetrics, QueueTask, HistoryEntry, AutopilotStatus, ServerStatus, LogEntry, GitGraphData } from './types'

async function fetchJSON<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}: ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export function GetMetrics(): Promise<DashboardMetrics> {
  return fetchJSON<DashboardMetrics>('/api/v1/metrics')
}

export function GetQueueTasks(): Promise<QueueTask[]> {
  return fetchJSON<QueueTask[]>('/api/v1/queue')
}

export function GetHistory(limit: number): Promise<HistoryEntry[]> {
  return fetchJSON<HistoryEntry[]>(`/api/v1/history?limit=${limit}`)
}

export function GetLogs(limit: number): Promise<LogEntry[]> {
  return fetchJSON<LogEntry[]>(`/api/v1/logs?limit=${limit}`)
}

export function GetAutopilotStatus(): Promise<AutopilotStatus> {
  return fetchJSON<AutopilotStatus>('/api/v1/autopilot')
}

export function GetServerStatus(): Promise<ServerStatus> {
  return fetch('/api/v1/status')
    .then((res) => {
      if (!res.ok) return { running: false } as ServerStatus
      return res.json() as Promise<ServerStatus>
    })
    .catch(() => ({ running: false }) as ServerStatus)
}

export function GetGitGraph(limit: number): Promise<GitGraphData> {
  return fetchJSON<GitGraphData>(`/api/v1/gitgraph?limit=${limit}`)
}

export function OpenInBrowser(url: string): Promise<void> {
  window.open(url, '_blank')
  return Promise.resolve()
}
