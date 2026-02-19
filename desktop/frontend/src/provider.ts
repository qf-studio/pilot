// Runtime mode detection: Wails desktop app vs browser (HTTP gateway).
// Exports a unified `api` object with the same function signatures regardless of mode.

import * as wailsApi from './wailsjs'
import * as httpApi from './apijs'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const isWails = typeof window !== 'undefined' && !!(window as any).go?.main?.App

export const api = isWails ? wailsApi : httpApi
