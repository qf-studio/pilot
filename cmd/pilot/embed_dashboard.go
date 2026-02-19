//go:build embed_dashboard

package main

import "embed"

//go:embed all:dashboard_dist
var dashboardFS embed.FS

// dashboardEmbedded indicates whether the dashboard frontend is embedded in this build.
const dashboardEmbedded = true
