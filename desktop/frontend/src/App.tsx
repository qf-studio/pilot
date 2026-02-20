import React from 'react'
import { Header } from './components/Header'
import { MetricsCards } from './components/MetricsCards'
import { QueuePanel } from './components/QueuePanel'
import { AutopilotPanel } from './components/AutopilotPanel'
import { HistoryPanel } from './components/HistoryPanel'
import { LogsPanel } from './components/LogsPanel'
import { GitGraphPanel } from './components/GitGraphPanel'
import { useDashboard } from './hooks/useDashboard'
import { useGitGraph } from './hooks/useGitGraph'

function App() {
  const { metrics, queueTasks, history, autopilot, server, logs } = useDashboard()
  const gitGraph = useGitGraph()
  const isWails = !!(window as any).go?.main?.App

  return (
    <div className={`flex flex-col h-full bg-bg overflow-hidden ${isWails ? 'wails-mode' : 'browser-mode'}`}>
      {/* macOS hidden-inset titlebar spacer — only in Wails mode */}
      {isWails && <div className="h-7 shrink-0" />}

      {/* Two-column layout: dashboard stack (left) + git graph (right) */}
      <div className="flex-1 flex gap-1.5 min-h-0 overflow-hidden">
        {/* Left column: vertical stack matching TUI renderDashboard order */}
        <div className="flex-[2] flex flex-col min-w-0 min-h-0 overflow-y-auto">
          <Header serverRunning={server.running} version={server.version} />
          <div className="px-2 pb-1">
            <MetricsCards metrics={metrics} />
          </div>
          <div className="flex-1 flex flex-col gap-1.5 px-2 pb-2 min-h-0">
            <div className="min-h-[120px] flex flex-col">
              <QueuePanel tasks={queueTasks} />
            </div>
            <div className="shrink-0" style={{ minHeight: '100px' }}>
              <AutopilotPanel status={autopilot} />
            </div>
            <div className="flex-1 min-h-[100px] flex flex-col">
              <HistoryPanel entries={history} />
            </div>
            <div className="shrink-0" style={{ minHeight: '80px', maxHeight: '160px' }}>
              <LogsPanel entries={logs} />
            </div>
          </div>
        </div>

        {/* Right column: Git Graph full-height */}
        <div className="flex-[3] min-w-0 min-h-0 flex flex-col pr-2 pb-2">
          <GitGraphPanel data={gitGraph} />
        </div>
      </div>
    </div>
  )
}

export default App
