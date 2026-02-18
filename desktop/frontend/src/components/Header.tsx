import React from 'react'

interface HeaderProps {
  serverRunning: boolean
  version?: string
}

export function Header({ serverRunning, version }: HeaderProps) {
  return (
    <div className="flex items-center justify-between px-3 py-2 border-b border-border">
      <div className="text-steel font-mono font-bold tracking-widest text-sm">
        PILOT
      </div>
      <div className="flex items-center gap-3 text-[10px]">
        {version && (
          <span className="text-gray">{version}</span>
        )}
        <span className={`flex items-center gap-1 ${serverRunning ? 'text-sage' : 'text-gray'}`}>
          <span className={`inline-block w-1.5 h-1.5 rounded-full ${serverRunning ? 'bg-sage pulse' : 'bg-gray'}`} />
          {serverRunning ? 'daemon running' : 'daemon offline'}
        </span>
      </div>
    </div>
  )
}
