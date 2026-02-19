//go:build !embed_dashboard

package main

import "embed"

// dashboardFS is an empty filesystem when building without embedded dashboard.
var dashboardFS embed.FS

// dashboardEmbedded indicates whether the dashboard frontend is embedded in this build.
const dashboardEmbedded = false
