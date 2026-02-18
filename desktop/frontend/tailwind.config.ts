import type { Config } from 'tailwindcss'

export default {
  content: ['./src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        steel: '#7eb8da',
        sage: '#7ec699',
        rose: '#d48a8a',
        slate: '#3d4450',
        midgray: '#8b949e',
        gray: '#6e7681',
        amber: '#d4a054',
        lightgray: '#c9d1d9',
        bg: '#1e222a',
        card: '#252a35',
        border: '#2d3340',
      },
      fontFamily: {
        mono: ['SF Mono', 'Menlo', 'Monaco', 'Consolas', 'monospace'],
      },
    },
  },
  plugins: [],
} satisfies Config
