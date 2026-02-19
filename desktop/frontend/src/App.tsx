import React from 'react'
import { Header } from './components/Header'
import { MetricsCards } from './components/MetricsCards'
import { QueuePanel } from './components/QueuePanel'
import { AutopilotPanel } from './components/AutopilotPanel'
import { HistoryPanel } from './components/HistoryPanel'
import { LogsPanel } from './components/LogsPanel'
import { useDashboard } from './hooks/useDashboard'

function App() {
  const { metrics, queueTasks, history, autopilot, server, logs } = useDashboard()
  const isWails = !!(window as any).go?.main?.App

  return (
    <div className={`flex flex-col h-full bg-bg overflow-hidden ${isWails ? 'wails-mode' : 'browser-mode'}`}>
      {/* macOS hidden-inset titlebar spacer — only in Wails mode */}
      {isWails && <div className="h-7 shrink-0" />}

      <Header serverRunning={server.running} version={server.version} />

      {/* Metrics row */}
      <MetricsCards metrics={metrics} />

      {/* Main content — fills remaining space, no overflow */}
      <div className="flex-1 flex flex-col gap-1.5 px-2 pb-2 min-h-0 overflow-hidden">
        {/* Middle row: Queue (2/3) + History (1/3) */}
        <div className="flex-1 flex gap-1.5 min-h-0">
          <div className="flex-[2] min-w-0 min-h-0 flex flex-col">
            <QueuePanel tasks={queueTasks} />
          </div>
          <div className="flex-1 min-w-0 min-h-0 flex flex-col">
            <HistoryPanel entries={history} />
          </div>
        </div>

        {/* Bottom row: Autopilot (1/3) + Logs (2/3) */}
        <div className="shrink-0 flex gap-1.5" style={{ height: '30%', minHeight: '120px', maxHeight: '180px' }}>
          <div className="flex-1 min-w-0 min-h-0 flex flex-col">
            <AutopilotPanel status={autopilot} />
          </div>
          <div className="flex-[2] min-w-0 min-h-0 flex flex-col">
            <LogsPanel entries={logs} />
          </div>
        </div>
      </div>
    </div>
  )
}

export default App
