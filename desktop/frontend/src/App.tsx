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
      <Header serverRunning={server.running} version={server.version} />

      {/* Two-column layout */}
      <div className="flex flex-1 min-h-0 gap-1.5 px-2 pb-2">
        {/* Left column */}
        <div className="flex flex-col flex-1 min-w-0 min-h-0 gap-1.5">
          <MetricsCards metrics={metrics} />
          <QueuePanel tasks={queueTasks} />
          <AutopilotPanel status={autopilot} />
          <HistoryPanel entries={history} />
          <LogsPanel entries={logs} />
        </div>

        {/* Right column */}
        <div className="flex flex-1 min-w-0 min-h-0">
          <GitGraphPanel data={gitGraph} />
        </div>
      </div>
    </div>
  )
}

export default App
