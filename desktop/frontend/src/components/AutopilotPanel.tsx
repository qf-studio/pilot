import React from 'react'
import { Card } from './ui/Card'
import { api } from '../provider'

const { OpenInBrowser } = api
import type { AutopilotStatus, ActivePR } from '../types'

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

interface PRRowProps {
  pr: ActivePR
}

function PRRow({ pr }: PRRowProps) {
  const icon = STAGE_ICONS[pr.stage] ?? '?'
  const color = STAGE_COLORS[pr.stage] ?? 'text-midgray'

  return (
    <div
      className="flex items-center gap-1 cursor-pointer hover:bg-slate/30 rounded px-1 py-px transition-colors"
      onClick={() => pr.url && OpenInBrowser(pr.url)}
    >
      <span className={`text-[10px] font-bold ${color}`}>{icon}</span>
      <span className="text-steel text-[10px]">#{pr.number}</span>
      <span className="text-midgray text-[10px] truncate flex-1">{pr.branchName}</span>
      <span className={`text-[10px] ${color}`}>{pr.stage.replace('_', ' ')}</span>
    </div>
  )
}

function DotLeaderRow({ label, value, valueColor = 'text-lightgray' }: { label: string; value: string; valueColor?: string }) {
  return (
    <div className="flex items-baseline text-[10px] leading-tight">
      <span className="text-midgray shrink-0">{label}</span>
      <span className="flex-1 overflow-hidden whitespace-nowrap text-slate mx-0.5" style={{ lineHeight: '1' }}>
        {'·'.repeat(80)}
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
    <Card title="AUTOPILOT" className="shrink-0">
      <div className="overflow-y-auto h-full log-scroll">
        {!status.enabled && status.activePRs.length === 0 ? (
          <div className="text-gray text-[10px]">autopilot inactive</div>
        ) : (
          <div className="space-y-0.5">
            <DotLeaderRow label="Environment" value={status.environment || 'dev'} />
            <DotLeaderRow label="Post-merge" value={status.autoRelease ? 'auto-release' : 'none'} />
            <DotLeaderRow
              label="Auto-release"
              value={status.autoRelease ? 'enabled' : 'disabled'}
              valueColor={status.autoRelease ? 'text-sage' : 'text-gray'}
            />
            <DotLeaderRow label="Active PRs" value={String(status.activePRs.length)} />
            {status.failureCount > 0 && (
              <DotLeaderRow label="Failures" value={String(status.failureCount)} valueColor="text-amber" />
            )}
            {status.activePRs.length > 0 && (
              <div className="mt-1 space-y-0.5 border-t border-border pt-1">
                {status.activePRs.map((pr) => (
                  <PRRow key={pr.number} pr={pr} />
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </Card>
  )
}
