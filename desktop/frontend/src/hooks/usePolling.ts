import { useState, useEffect } from 'react'

export interface PollingState {
  tick: number
  isBlinking: boolean
}

// usePolling provides a 1-second tick counter for coordinating CSS animations.
export function usePolling(): PollingState {
  const [tick, setTick] = useState(0)

  useEffect(() => {
    const id = setInterval(() => {
      setTick((t) => t + 1)
    }, 1000)
    return () => clearInterval(id)
  }, [])

  return {
    tick,
    isBlinking: tick % 2 === 0,
  }
}
