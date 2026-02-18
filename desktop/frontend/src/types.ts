export interface DashboardMetrics {
  totalTokens: number
  inputTokens: number
  outputTokens: number
  totalCostUSD: number
  totalTasks: number
  succeededTasks: number
  failedTasks: number
  tokenSparkline: number[]
  costSparkline: number[]
  queueSparkline: number[]
}

export interface QueueTask {
  id: string
  issueID: string
  title: string
  status: 'running' | 'queued' | 'pending' | 'done' | 'failed'
  progress: number
  prURL?: string
  issueURL?: string
  projectPath: string
  createdAt: string
}

export interface HistoryEntry {
  id: string
  issueID: string
  title: string
  status: string
  prURL?: string
  projectPath: string
  completedAt: string
  durationMs: number
  epicID?: string
  subIssues?: HistoryEntry[]
}

export interface ActivePR {
  number: number
  url: string
  stage: string
  ciStatus?: string
  error?: string
  branchName: string
}

export interface AutopilotStatus {
  enabled: boolean
  environment: string
  autoRelease: boolean
  activePRs: ActivePR[]
  failureCount: number
}

export interface ServerStatus {
  running: boolean
  version?: string
  gatewayURL?: string
}
