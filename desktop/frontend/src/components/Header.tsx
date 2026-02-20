import React from 'react'

const PILOT_LOGO = `██████╗ ██╗██╗      ██████╗ ████████╗
██╔══██╗██║██║     ██╔═══██╗╚══██╔══╝
██████╔╝██║██║     ██║   ██║   ██║
██╔═══╝ ██║██║     ██║   ██║   ██║
██║     ██║███████╗╚██████╔╝   ██║
╚═╝     ╚═╝╚══════╝ ╚═════╝    ╚═╝`

interface HeaderProps {
  serverRunning: boolean
  version?: string
}

export function Header({ serverRunning, version }: HeaderProps) {
  return (
    <div className="px-3 py-2 border-b border-border">
      <pre
        className="leading-none text-[8px] font-mono whitespace-pre"
        style={{ color: '#7eb8da' }}
      >
        {PILOT_LOGO}
      </pre>
      <div className="flex items-center gap-3 mt-1">
        {version && (
          <span className="text-gray text-[10px] font-mono">
            {version}
          </span>
        )}
        <span className={`flex items-center gap-1 text-[10px] ${serverRunning ? 'text-sage' : 'text-gray'}`}>
          <span className={`inline-block w-1.5 h-1.5 rounded-full ${serverRunning ? 'bg-sage pulse' : 'bg-gray'}`} />
          {serverRunning ? 'daemon running' : 'daemon offline'}
        </span>
      </div>
    </div>
  )
}
