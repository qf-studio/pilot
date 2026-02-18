import React from 'react'
import { Card } from './ui/Card'
import { OpenInBrowser } from '../wailsjs'
import type { AutopilotStatus, ActivePR } from '../types'

// Stage icon mapping — matches TUI autopilot panel
const STAGE_ICONS: Record<string, string> = {
  created: '+',
  waiting_ci: '~',
  ci_passed: '*',
  ci_failed: 'x',
  awaiting_approval: '?',
  merging: '>',
  releasing: '^',
  failed: '!',
}

const STAGE_COLORS: Record<string, string> = {
  created: 'text-midgray',
  waiting_ci: 'text-amber',
  ci_passed: 'text-sage',
  ci_failed: 'text-rose',
  awaiting_approval: 'text-amber',
  merging: 'text-steel',
  releasing: 'text-steel',
  failed: 'text-rose',
}

function stageIcon(stage: string): string {
  return STAGE_ICONS[stage] ?? '?'
}

function stageColor(stage: string): string {
  return STAGE_COLORS[stage] ?? 'text-midgray'
}

interface PRRowProps {
  pr: ActivePR
}

function PRRow({ pr }: PRRowProps) {
  const icon = stageIcon(pr.stage)
  const color = stageColor(pr.stage)

  return (
    <div className="space-y-0.5">
      <div
        className="flex items-center gap-1.5 cursor-pointer hover:bg-slate/30 rounded px-1 py-0.5 transition-colors"
        onClick={() => pr.url && OpenInBrowser(pr.url)}
      >
        <span className={`text-[11px] font-bold ${color}`}>{icon}</span>
        <span className="text-steel text-[10px]">PR #{pr.number}</span>
        <span className="text-midgray text-[10px] truncate flex-1">{pr.branchName}</span>
        <span className={`text-[10px] ${color}`}>{pr.stage.replace('_', ' ')}</span>
      </div>
      {pr.ciStatus && pr.stage === 'waiting_ci' && (
        <div className="text-amber text-[10px] pl-5">CI: {pr.ciStatus}</div>
      )}
      {pr.error && (
        <div className="text-rose text-[10px] pl-5 truncate">{pr.error}</div>
      )}
    </div>
  )
}

interface DotRowProps {
  label: string
  value: string
  valueColor?: string
}

function DotRow({ label, value, valueColor = 'text-lightgray' }: DotRowProps) {
  return (
    <div className="flex items-baseline gap-0 text-[10px]">
      <span className="text-midgray shrink-0">{label}</span>
      <span className="flex-1 text-slate overflow-hidden whitespace-nowrap">
        {' '}
        {'·'.repeat(30)}
      </span>
      <span className={`shrink-0 ${valueColor}`}>{value}</span>
    </div>
  )
}

interface AutopilotPanelProps {
  status: AutopilotStatus
}

export function AutopilotPanel({ status }: AutopilotPanelProps) {
  return (
    <Card title="AUTOPILOT">
      {!status.enabled && status.activePRs.length === 0 ? (
        <div className="text-gray text-[10px]">autopilot inactive</div>
      ) : (
        <div className="space-y-1">
          <DotRow label="mode" value={status.environment || 'dev'} />
          <DotRow
            label="auto-release"
            value={status.autoRelease ? 'enabled' : 'disabled'}
            valueColor={status.autoRelease ? 'text-sage' : 'text-gray'}
          />
          {status.failureCount > 0 && (
            <DotRow
              label="failures"
              value={String(status.failureCount)}
              valueColor="text-amber"
            />
          )}
          {status.activePRs.length > 0 && (
            <div className="mt-1.5 space-y-1 border-t border-border pt-1">
              {status.activePRs.map((pr) => (
                <PRRow key={pr.number} pr={pr} />
              ))}
            </div>
          )}
        </div>
      )}
    </Card>
  )
}
