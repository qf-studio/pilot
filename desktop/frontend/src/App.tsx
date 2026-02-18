import React from 'react'
import { Header } from './components/Header'
import { MetricsCards } from './components/MetricsCards'
import { QueuePanel } from './components/QueuePanel'
import { AutopilotPanel } from './components/AutopilotPanel'
import { HistoryPanel } from './components/HistoryPanel'
import { LogsPanel } from './components/LogsPanel'
import { useDashboard } from './hooks/useDashboard'

function App() {
  const { metrics, queueTasks, history, autopilot, server } = useDashboard()

  return (
    <div className="flex flex-col h-full bg-bg overflow-hidden">
      {/* macOS hidden-inset titlebar spacer */}
      <div className="h-7 shrink-0" />

      <Header serverRunning={server.running} version={server.version} />

      {/* Metrics row */}
      <MetricsCards metrics={metrics} />

      {/* Main panels — flex-1 fills remaining space */}
      <div className="flex-1 flex flex-col gap-2 px-2 pb-2 min-h-0 overflow-hidden">
        {/* Queue — largest panel, grows */}
        <div className="flex-1 min-h-0 flex flex-col">
          <QueuePanel tasks={queueTasks} />
        </div>

        {/* Bottom row: Autopilot + History */}
        <div className="flex gap-2 shrink-0">
          <div className="flex-1 min-w-0">
            <AutopilotPanel status={autopilot} />
          </div>
          <div className="flex-1 min-w-0">
            <HistoryPanel entries={history} />
          </div>
        </div>

        {/* Logs — collapsed at bottom */}
        <div className="shrink-0">
          <LogsPanel entries={[]} />
        </div>
      </div>
    </div>
  )
}

export default App
